import asyncio
import logging
from datetime import UTC, datetime
from typing import Any

import asyncpg

from app.config import Settings

log = logging.getLogger(__name__)


class DatabaseUnavailable(Exception):
    pass


async def create_pool(settings: Settings) -> asyncpg.Pool:
    return await asyncpg.create_pool(
        dsn=settings.database_url,
        min_size=settings.db_pool_min,
        max_size=settings.db_pool_max,
        command_timeout=settings.db_command_timeout,
        max_inactive_connection_lifetime=300,
        timeout=5,
        server_settings={"application_name": "orders-api"},
    )


async def close_pool(pool: asyncpg.Pool, timeout: float = 10.0) -> None:
    try:
        await asyncio.wait_for(pool.close(), timeout)
    except TimeoutError:
        log.warning("pool close timed out; terminating connections")
        pool.terminate()


class Repository:
    def __init__(self, pool: asyncpg.Pool, acquire_timeout: float):
        self._pool = pool
        self._acquire_timeout = acquire_timeout

    def _acquire(self):
        return self._pool.acquire(timeout=self._acquire_timeout)

    async def create_order(self, customer_id: int, items: list[tuple[str, int]]) -> int:
        skus = [sku for sku, _ in items]
        qtys = [qty for _, qty in items]
        async with self._acquire() as conn, conn.transaction():
            order_id = await conn.fetchval(
                "INSERT INTO orders (customer_id) VALUES ($1) RETURNING id",
                customer_id,
            )
            await conn.execute(
                """
                INSERT INTO order_items (order_id, sku, qty)
                SELECT $1, s, q FROM unnest($2::text[], $3::int[]) AS t(s, q)
                """,
                order_id,
                skus,
                qtys,
            )
        return order_id

    async def get_order(self, order_id: int) -> dict[str, Any] | None:
        async with self._acquire() as conn:
            row = await conn.fetchrow(
                "SELECT id, customer_id, status, created_at FROM orders WHERE id = $1",
                order_id,
            )
            if row is None:
                return None
            items = await conn.fetch(
                "SELECT sku, qty FROM order_items WHERE order_id = $1 ORDER BY sku",
                order_id,
            )
        return _order_dict(row, [{"sku": i["sku"], "qty": i["qty"]} for i in items])

    async def get_order_lines(self, order_id: int) -> list[tuple[str, int]] | None:
        """Returns the order's (sku, qty) pairs, or None if the order does not exist."""
        async with self._acquire() as conn:
            exists = await conn.fetchval("SELECT 1 FROM orders WHERE id = $1", order_id)
            if exists is None:
                return None
            rows = await conn.fetch(
                "SELECT sku, qty FROM order_items WHERE order_id = $1", order_id
            )
        return [(r["sku"], r["qty"]) for r in rows]

    async def list_customer_orders(
        self,
        customer_id: int,
        limit: int,
        after: tuple[datetime, int] | None,
    ) -> tuple[list[dict[str, Any]], tuple[datetime, int] | None]:
        """Keyset pagination over (created_at DESC, id DESC), served by
        orders_customer_created. Returns the page and the key to continue from."""
        async with self._acquire() as conn:
            if after is None:
                rows = await conn.fetch(
                    """
                    SELECT id, customer_id, status, created_at FROM orders
                    WHERE customer_id = $1
                    ORDER BY created_at DESC, id DESC
                    LIMIT $2
                    """,
                    customer_id,
                    limit + 1,
                )
            else:
                rows = await conn.fetch(
                    """
                    SELECT id, customer_id, status, created_at FROM orders
                    WHERE customer_id = $1 AND (created_at, id) < ($2, $3)
                    ORDER BY created_at DESC, id DESC
                    LIMIT $4
                    """,
                    customer_id,
                    after[0],
                    after[1],
                    limit + 1,
                )
            has_more = len(rows) > limit
            rows = rows[:limit]
            items_by_order: dict[int, list[dict[str, Any]]] = {r["id"]: [] for r in rows}
            if rows:
                item_rows = await conn.fetch(
                    """
                    SELECT order_id, sku, qty FROM order_items
                    WHERE order_id = ANY($1::bigint[])
                    ORDER BY order_id, sku
                    """,
                    list(items_by_order),
                )
                for i in item_rows:
                    items_by_order[i["order_id"]].append({"sku": i["sku"], "qty": i["qty"]})

        orders = [_order_dict(r, items_by_order[r["id"]]) for r in rows]
        next_key = (rows[-1]["created_at"], rows[-1]["id"]) if has_more else None
        return orders, next_key


def _order_dict(row: asyncpg.Record, items: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "id": row["id"],
        "customer_id": row["customer_id"],
        "status": row["status"],
        "created_at": rfc3339(row["created_at"]),
        "items": items,
    }


def rfc3339(dt: datetime) -> str:
    return dt.astimezone(UTC).isoformat().replace("+00:00", "Z")
