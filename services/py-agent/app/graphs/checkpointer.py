"""LangGraph PostgreSQL Checkpointer manager with Windows-safe sync ConnectionPool."""

from __future__ import annotations

from typing import Any

from langgraph.checkpoint.memory import MemorySaver
from langgraph.checkpoint.postgres import PostgresSaver
from psycopg.rows import dict_row
from psycopg_pool import ConnectionPool


class CheckpointerManager:
    """Manages PostgreSQL ConnectionPool and LangGraph PostgresSaver (or MemorySaver for tests)."""

    def __init__(self, database_url: str = "") -> None:
        self.database_url = database_url.strip()
        self.pool: ConnectionPool | None = None
        self.checkpointer: Any = None

    def ensure_pool(self) -> ConnectionPool | None:
        """Open and return the synchronous PostgreSQL ConnectionPool when configured."""
        if not self.database_url:
            return None
        if self.pool is None:
            self.pool = ConnectionPool(
                conninfo=self.database_url,
                min_size=1,
                max_size=10,
                open=False,
                kwargs={
                    "autocommit": True,
                    "prepare_threshold": 0,
                    "row_factory": dict_row,
                },
            )
            self.pool.open(wait=True)
        return self.pool

    def open(self) -> Any:
        """Open connection pool and run PostgresSaver.setup() idempotently."""
        if self.checkpointer is not None:
            return self.checkpointer

        pool = self.ensure_pool()
        if pool is None:
            self.checkpointer = MemorySaver()
            return self.checkpointer

        saver = PostgresSaver(pool)
        saver.setup()
        self.checkpointer = saver
        return self.checkpointer

    def close(self) -> None:
        """Close the underlying connection pool cleanly."""
        if self.pool is not None:
            self.pool.close()
            self.pool = None
        self.checkpointer = None
