import os
from dataclasses import dataclass


def _int(name: str, default: int) -> int:
    return int(os.environ.get(name, default))


def _float(name: str, default: float) -> float:
    return float(os.environ.get(name, default))


@dataclass(frozen=True, slots=True)
class Settings:
    database_url: str
    pricing_url: str

    # Pool size is per worker process. Budget: 8 replicas x 2 workers x 4 = 64
    # of the 80 allowed connections, leaving headroom for rolling deploys where
    # old and new containers briefly overlap.
    db_pool_min: int
    db_pool_max: int
    db_acquire_timeout: float
    db_command_timeout: float

    pricing_request_timeout: float
    pricing_total_timeout: float
    pricing_max_concurrency: int
    pricing_breaker_threshold: int
    pricing_breaker_cooldown: float

    @classmethod
    def from_env(cls) -> "Settings":
        database_url = os.environ.get("DATABASE_URL")
        pricing_url = os.environ.get("PRICING_URL")
        if not database_url:
            raise RuntimeError("DATABASE_URL is required")
        if not pricing_url:
            raise RuntimeError("PRICING_URL is required")
        return cls(
            database_url=database_url,
            pricing_url=pricing_url.rstrip("/"),
            db_pool_min=_int("DB_POOL_MIN", 1),
            db_pool_max=_int("DB_POOL_MAX", 4),
            db_acquire_timeout=_float("DB_ACQUIRE_TIMEOUT", 2.0),
            db_command_timeout=_float("DB_COMMAND_TIMEOUT", 5.0),
            pricing_request_timeout=_float("PRICING_REQUEST_TIMEOUT", 1.0),
            pricing_total_timeout=_float("PRICING_TOTAL_TIMEOUT", 2.0),
            pricing_max_concurrency=_int("PRICING_MAX_CONCURRENCY", 16),
            pricing_breaker_threshold=_int("PRICING_BREAKER_THRESHOLD", 5),
            pricing_breaker_cooldown=_float("PRICING_BREAKER_COOLDOWN", 5.0),
        )
