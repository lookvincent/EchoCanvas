"""Deterministic local 1536-dim feature hashing embeddings for pgvector HNSW indexing."""

from __future__ import annotations

import hashlib
import math
import re

EMBEDDING_DIM = 1536


def _tokenize(text: str) -> list[str]:
    lowered = text.lower()
    words = re.findall(r"[a-z0-9_]+", lowered)
    cjk_chars = re.findall(r"[\u4e00-\u9fff]", lowered)
    cjk_bigrams = [cjk_chars[i] + cjk_chars[i + 1] for i in range(len(cjk_chars) - 1)]
    return words + cjk_chars + cjk_bigrams


def compute_deterministic_embedding(text: str, dim: int = EMBEDDING_DIM) -> list[float]:
    """Build an L2-normalized 1536-dimensional semantic-token hashed vector."""
    vec = [0.0] * dim
    tokens = _tokenize(text)
    if not tokens:
        vec[0] = 1.0
        return vec

    for token in tokens:
        digest = hashlib.sha256(token.encode("utf-8")).digest()
        bucket = int.from_bytes(digest[:4], "big") % dim
        sign = 1.0 if (digest[4] & 1) == 0 else -1.0
        weight = 1.5 if len(token) >= 2 else 1.0
        vec[bucket] += sign * weight

    norm = math.sqrt(sum(v * v for v in vec))
    if norm == 0.0:
        vec[0] = 1.0
        return vec
    return [v / norm for v in vec]


def format_pgvector(vec: list[float]) -> str:
    """Format float vector into PostgreSQL vector literal syntax."""
    return "[" + ",".join(f"{v:.6f}" for v in vec) + "]"
