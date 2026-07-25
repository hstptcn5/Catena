from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass
from typing import Any


@dataclass
class CatenaClient:
    base_url: str
    api_key: str = ""

    def __post_init__(self) -> None:
        self.base_url = self.base_url.rstrip("/")

    def query(self, sql: str, params: list[Any] | None = None) -> dict[str, Any]:
        return self._post("/query", {"sql": sql, "params": params or []})

    def transaction(self, statements: list[dict[str, Any]]) -> list[dict[str, Any]]:
        payload = self._post("/transaction", {"statements": statements})
        return payload["results"]

    def _post(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        body = json.dumps(payload).encode("utf-8")
        headers = {"Content-Type": "application/json"}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        req = urllib.request.Request(
            f"{self.base_url}{path}",
            data=body,
            headers=headers,
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as res:
                return json.loads(res.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            details = exc.read().decode("utf-8")
            raise RuntimeError(f"Catena request failed: {details}") from exc
