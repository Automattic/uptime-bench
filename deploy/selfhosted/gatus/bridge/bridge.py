import os
import re
import threading
from typing import Dict, List, Optional

import requests
import yaml
from fastapi import FastAPI, Header, HTTPException, Query
from pydantic import BaseModel, Field


TOKEN = os.environ.get("BRIDGE_TOKEN", "")
GATUS_URL = os.environ.get("GATUS_URL", "http://gatus:8080").rstrip("/")
CONFIG_FILE = os.environ.get("GATUS_CONFIG_FILE", "/config/uptime-bench.yaml")

ID_RE = re.compile(r"^[A-Za-z0-9_.-]+$")
LOCK = threading.Lock()

app = FastAPI(title="uptime-bench Gatus bridge")


class MonitorIn(BaseModel):
    id: str = Field(min_length=1, max_length=120)
    name: Optional[str] = Field(default=None, max_length=160)
    url: str
    interval_seconds: int = Field(default=60, ge=1)
    method: str = "GET"
    keyword: Optional[str] = None
    keyword_check: str = "present"
    response_time_threshold_ms: int = 0
    headers: Dict[str, str] = Field(default_factory=dict)


def bootstrap_endpoint() -> dict:
    return {
        "name": "uptime-bench-bootstrap",
        "group": "uptime-bench-internal",
        "url": "http://127.0.0.1:8080/health",
        "interval": "5m",
        "conditions": ["[STATUS] == 200"],
    }


def require_auth(authorization: Optional[str]) -> None:
    if not TOKEN:
        raise HTTPException(status_code=500, detail="BRIDGE_TOKEN is not configured")
    if authorization != f"Bearer {TOKEN}":
        raise HTTPException(status_code=401, detail="unauthorized")


def load_config() -> dict:
    if not os.path.exists(CONFIG_FILE):
        return {"endpoints": []}
    with open(CONFIG_FILE, "r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    endpoints = data.get("endpoints")
    if endpoints is None:
        data["endpoints"] = []
    if not isinstance(data["endpoints"], list):
        raise HTTPException(status_code=500, detail="invalid endpoints config")
    if not any(e.get("name") == "uptime-bench-bootstrap" for e in data["endpoints"]):
        data["endpoints"].insert(0, bootstrap_endpoint())
    return data


def save_config(data: dict) -> None:
    endpoints = [e for e in data.get("endpoints", []) if e.get("name") != "uptime-bench-bootstrap"]
    data["endpoints"] = [bootstrap_endpoint(), *endpoints]
    os.makedirs(os.path.dirname(CONFIG_FILE), exist_ok=True)
    tmp = f"{CONFIG_FILE}.tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        yaml.safe_dump(data, f, sort_keys=False)
    os.replace(tmp, CONFIG_FILE)


def endpoint_key(monitor_id: str) -> str:
    return f"uptime-bench_{monitor_id}"


def gatus_status_key(monitor_id: str) -> str:
    # Gatus normalizes endpoint keys by joining group/name and replacing
    # separators such as "_" with "-".
    name = endpoint_key(monitor_id)
    for ch in (" ", "/", "_", ".", "#", "+", "&"):
        name = name.replace(ch, "-")
    return f"uptime-bench_{name}"


def monitor_endpoint(req: MonitorIn) -> dict:
    if not ID_RE.match(req.id):
        raise HTTPException(status_code=400, detail="id contains unsupported characters")
    method = req.method.upper()
    if method not in {"GET", "HEAD", "POST", "PUT", "PATCH"}:
        raise HTTPException(status_code=400, detail="unsupported method")

    conditions: List[str] = ["[STATUS] == 200"]
    if req.keyword:
        pattern = req.keyword.replace("\\", "\\\\").replace("*", "\\*")
        if req.keyword_check == "present":
            conditions.append(f"[BODY] == pat(*{pattern}*)")
        elif req.keyword_check == "absent":
            conditions.append(f"[BODY] != pat(*{pattern}*)")
        else:
            raise HTTPException(status_code=400, detail="unsupported keyword_check")
    if req.response_time_threshold_ms > 0:
        conditions.append(f"[RESPONSE_TIME] < {req.response_time_threshold_ms}")

    endpoint = {
        "name": endpoint_key(req.id),
        "group": "uptime-bench",
        "url": req.url,
        "interval": f"{req.interval_seconds}s",
        "method": method,
        "conditions": conditions,
    }
    if req.headers:
        endpoint["headers"] = req.headers
    return endpoint


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/monitors")
def list_monitors(authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    with LOCK:
        return {"monitors": load_config().get("endpoints", [])}


@app.post("/monitors")
def create_monitor(req: MonitorIn, authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    endpoint = monitor_endpoint(req)
    with LOCK:
        data = load_config()
        endpoints = [e for e in data.get("endpoints", []) if e.get("name") != endpoint["name"]]
        endpoints.append(endpoint)
        data["endpoints"] = endpoints
        save_config(data)
    return {"id": req.id, "monitor_id": endpoint["name"], "endpoint": endpoint}


@app.delete("/monitors/{monitor_id}")
def delete_monitor(monitor_id: str, authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    if not ID_RE.match(monitor_id):
        raise HTTPException(status_code=400, detail="monitor_id contains unsupported characters")
    name = endpoint_key(monitor_id)
    with LOCK:
        data = load_config()
        before = len(data.get("endpoints", []))
        data["endpoints"] = [e for e in data.get("endpoints", []) if e.get("name") != name]
        save_config(data)
    return {"id": monitor_id, "deleted": before != len(data["endpoints"])}


@app.get("/monitors/{monitor_id}/statuses")
def statuses(
    monitor_id: str,
    authorization: Optional[str] = Header(default=None),
    page: int = Query(default=1, ge=1),
):
    require_auth(authorization)
    if not ID_RE.match(monitor_id):
        raise HTTPException(status_code=400, detail="monitor_id contains unsupported characters")
    key = gatus_status_key(monitor_id)
    resp = requests.get(
        f"{GATUS_URL}/api/v1/endpoints/{key}/statuses",
        params={"page": page},
        timeout=15,
    )
    if resp.status_code == 404:
        return {"endpoint_key": key, "results": []}
    resp.raise_for_status()
    data = resp.json()
    if isinstance(data, list):
        data = {"results": data}
    data["endpoint_key"] = key
    return data
