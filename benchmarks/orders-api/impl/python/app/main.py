import base64
import binascii
import logging
import re
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta

import asyncpg
from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field, StrictInt, StrictStr, field_validator
from starlette.exceptions import HTTPException

from app.config import Settings
from app.db import Repository, close_pool, create_pool
from app.pricing import PricingClient, PricingUnavailable, UnknownSku

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("orders-api")

MAX_BIGINT = 2**63 - 1
MAX_INT = 2**31 - 1
DEFAULT_LIMIT = 20
MAX_LIMIT = 100
_ID_RE = re.compile(r"[0-9]{1,19}")
_EPOCH = datetime(1970, 1, 1, tzinfo=UTC)


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = Settings.from_env()
    pool = await create_pool(settings)
    pricing = PricingClient(settings)
    app.state.repo = Repository(pool, settings.db_acquire_timeout)
    app.state.pricing = pricing
    log.info("started; db pool max=%d", settings.db_pool_max)
    try:
        yield
    finally:
        # Uvicorn has already drained in-flight requests when we get here.
        await pricing.aclose()
        await close_pool(pool)
        log.info("shut down cleanly")


app = FastAPI(
    title="orders-api",
    lifespan=lifespan,
    docs_url=None,
    redoc_url=None,
    openapi_url=None,
)


def error(status: int, message: str) -> JSONResponse:
    return JSONResponse(status_code=status, content={"error": message})


# ---------------------------------------------------------------- errors


@app.exception_handler(HTTPException)
async def http_exception_handler(_: Request, exc: HTTPException) -> JSONResponse:
    return JSONResponse(
        status_code=exc.status_code,
        content={"error": str(exc.detail)},
        headers=getattr(exc, "headers", None),
    )


@app.exception_handler(RequestValidationError)
async def validation_exception_handler(_: Request, exc: RequestValidationError) -> JSONResponse:
    errs = exc.errors()
    if not errs:
        return error(400, "invalid request")
    first = errs[0]
    loc = ".".join(str(p) for p in first.get("loc", ()) if p not in ("body", "query", "path"))
    msg = first.get("msg", "invalid value")
    return error(400, f"{loc}: {msg}" if loc else msg)


async def db_unavailable_handler(_: Request, exc: Exception) -> JSONResponse:
    log.warning("database unavailable: %r", exc)
    return error(503, "database unavailable")


# Pool acquire/command timeouts surface as TimeoutError; connection problems as
# the asyncpg/OS errors below. All are transient: tell the client to retry.
for _exc in (
    TimeoutError,
    OSError,
    asyncpg.PostgresConnectionError,
    asyncpg.InterfaceError,
    asyncpg.TooManyConnectionsError,
    asyncpg.CannotConnectNowError,
):
    app.add_exception_handler(_exc, db_unavailable_handler)


@app.exception_handler(Exception)
async def unhandled_exception_handler(_: Request, exc: Exception) -> JSONResponse:
    log.exception("unhandled error", exc_info=exc)
    return error(500, "internal error")


# ---------------------------------------------------------------- models


class ItemIn(BaseModel):
    sku: StrictStr = Field(min_length=1, max_length=128)
    qty: StrictInt = Field(ge=1, le=MAX_INT)

    @field_validator("sku")
    @classmethod
    def sku_no_nul(cls, v: str) -> str:
        if "\x00" in v:
            raise ValueError("sku must not contain NUL characters")
        return v


class OrderIn(BaseModel):
    customer_id: StrictInt = Field(gt=0, le=MAX_BIGINT)
    items: list[ItemIn] = Field(min_length=1, max_length=100)

    @field_validator("items")
    @classmethod
    def unique_skus(cls, v: list[ItemIn]) -> list[ItemIn]:
        if len({i.sku for i in v}) != len(v):
            raise ValueError("duplicate sku")
        return v


# ---------------------------------------------------------------- helpers


def parse_id(raw: str) -> int | None:
    """Returns the id if it is a positive bigint, None if it is numeric but out
    of range, and raises 400 if it isn't a number at all."""
    if not _ID_RE.fullmatch(raw):
        raise HTTPException(400, "id must be a positive integer")
    value = int(raw)
    return value if 1 <= value <= MAX_BIGINT else None


def encode_cursor(key: tuple[datetime, int]) -> str:
    created_at, order_id = key
    micros = (created_at - _EPOCH) // timedelta(microseconds=1)
    raw = f"{micros}.{order_id}".encode()
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def decode_cursor(cursor: str) -> tuple[datetime, int]:
    try:
        padded = cursor + "=" * (-len(cursor) % 4)
        micros_s, id_s = base64.urlsafe_b64decode(padded).decode().split(".")
        micros, order_id = int(micros_s), int(id_s)
        if not 1 <= order_id <= MAX_BIGINT:
            raise ValueError
        return _EPOCH + timedelta(microseconds=micros), order_id
    except (ValueError, binascii.Error, UnicodeDecodeError, OverflowError):
        raise HTTPException(400, "invalid cursor") from None


# ---------------------------------------------------------------- routes


@app.get("/healthz")
async def healthz():
    # Deliberately does not touch the database: a DB blip should not make the
    # load balancer pull every replica at once.
    return {"status": "ok"}


@app.post("/orders", status_code=201)
async def create_order(body: OrderIn, request: Request):
    repo: Repository = request.app.state.repo
    order_id = await repo.create_order(body.customer_id, [(i.sku, i.qty) for i in body.items])
    return JSONResponse(status_code=201, content={"id": order_id})


@app.get("/orders/{order_id}")
async def get_order(order_id: str, request: Request):
    oid = parse_id(order_id)
    if oid is None:
        return error(404, "order not found")
    order = await request.app.state.repo.get_order(oid)
    if order is None:
        return error(404, "order not found")
    return order


@app.get("/customers/{customer_id}/orders")
async def list_customer_orders(
    customer_id: str,
    request: Request,
    limit: int = DEFAULT_LIMIT,
    cursor: str = "",
):
    cid = parse_id(customer_id)
    if cid is None:
        return error(400, "customer id out of range")
    if not 1 <= limit <= MAX_LIMIT:
        return error(400, f"limit must be between 1 and {MAX_LIMIT}")
    after = decode_cursor(cursor) if cursor else None
    orders, next_key = await request.app.state.repo.list_customer_orders(cid, limit, after)
    return {"orders": orders, "next_cursor": encode_cursor(next_key) if next_key else ""}


@app.get("/orders/{order_id}/quote")
async def quote_order(order_id: str, request: Request):
    oid = parse_id(order_id)
    if oid is None:
        return error(404, "order not found")
    # The DB connection is released before calling pricing, so a slow pricing
    # service cannot starve the (small) connection pool.
    lines = await request.app.state.repo.get_order_lines(oid)
    if lines is None:
        return error(404, "order not found")
    pricing: PricingClient = request.app.state.pricing
    try:
        total = await pricing.total_cents(lines)
    except UnknownSku as e:
        return error(422, str(e))
    except PricingUnavailable as e:
        log.warning("quote for order %d failed: %s", oid, e)
        return error(503, "pricing service unavailable")
    return {"order_id": oid, "total_cents": total}
