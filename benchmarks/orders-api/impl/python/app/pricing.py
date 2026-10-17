import asyncio
import logging
import time
from urllib.parse import quote

import httpx

from app.config import Settings

log = logging.getLogger(__name__)


class UnknownSku(Exception):
    def __init__(self, sku: str):
        super().__init__(f"unknown sku: {sku}")
        self.sku = sku


class PricingUnavailable(Exception):
    pass


class PricingClient:
    """Client for the pricing service.

    Every quote is bounded by a total deadline, outbound concurrency is capped,
    and a small circuit breaker fails fast while the service is unhealthy so
    slow pricing calls can't pile up and exhaust the worker.
    """

    def __init__(self, settings: Settings):
        self._client = httpx.AsyncClient(
            base_url=settings.pricing_url,
            timeout=httpx.Timeout(
                settings.pricing_request_timeout,
                connect=min(0.5, settings.pricing_request_timeout),
            ),
            limits=httpx.Limits(max_connections=64, max_keepalive_connections=32),
        )
        self._total_timeout = settings.pricing_total_timeout
        self._max_concurrency = settings.pricing_max_concurrency
        self._breaker_threshold = settings.pricing_breaker_threshold
        self._breaker_cooldown = settings.pricing_breaker_cooldown
        self._consecutive_failures = 0
        self._open_until = 0.0

    async def aclose(self) -> None:
        await self._client.aclose()

    async def total_cents(self, lines: list[tuple[str, int]]) -> int:
        if time.monotonic() < self._open_until:
            raise PricingUnavailable("pricing service unavailable")

        sem = asyncio.Semaphore(self._max_concurrency)

        async def line_total(sku: str, qty: int) -> int:
            async with sem:
                return await self._price(sku) * qty

        try:
            async with asyncio.timeout(self._total_timeout):
                async with asyncio.TaskGroup() as tg:
                    tasks = [tg.create_task(line_total(sku, qty)) for sku, qty in lines]
        except TimeoutError:
            self._record_failure()
            raise PricingUnavailable("pricing service timed out") from None
        except ExceptionGroup as eg:
            unknown = eg.subgroup(UnknownSku)
            if unknown is not None:
                # The service answered correctly; this is not a health failure.
                self._record_success()
                raise _first_leaf(unknown) from None
            self._record_failure()
            raise PricingUnavailable("pricing service unavailable") from eg

        self._record_success()
        return sum(t.result() for t in tasks)

    async def _price(self, sku: str) -> int:
        try:
            resp = await self._client.get(f"/prices/{quote(sku, safe='')}")
        except httpx.HTTPError as e:
            raise PricingUnavailable(f"pricing request failed: {e!r}") from e
        if resp.status_code == 404:
            raise UnknownSku(sku)
        if resp.status_code != 200:
            raise PricingUnavailable(f"pricing returned {resp.status_code}")
        try:
            cents = resp.json()["cents"]
        except (ValueError, KeyError, TypeError) as e:
            raise PricingUnavailable("malformed pricing response") from e
        if type(cents) is not int or cents < 0:
            raise PricingUnavailable("malformed pricing response")
        return cents

    def _record_success(self) -> None:
        self._consecutive_failures = 0

    def _record_failure(self) -> None:
        self._consecutive_failures += 1
        if self._consecutive_failures >= self._breaker_threshold:
            if time.monotonic() >= self._open_until:
                log.warning("pricing circuit breaker open for %.1fs", self._breaker_cooldown)
            self._open_until = time.monotonic() + self._breaker_cooldown


def _first_leaf(eg: BaseExceptionGroup) -> BaseException:
    exc: BaseException = eg
    while isinstance(exc, BaseExceptionGroup):
        exc = exc.exceptions[0]
    return exc
