import os
import re
import time
from datetime import datetime, timezone
from typing import Dict, Optional

from fastapi import FastAPI, Header, HTTPException, Query
from pydantic import BaseModel, Field
from uptime_kuma_api import MonitorType, UptimeKumaApi


TOKEN = os.environ.get("BRIDGE_TOKEN", "")
KUMA_URL = os.environ.get("KUMA_URL", "http://uptime-kuma:3001").rstrip("/")
KUMA_USERNAME = os.environ.get("KUMA_USERNAME", "")
KUMA_PASSWORD = os.environ.get("KUMA_PASSWORD", "")

ID_RE = re.compile(r"^[A-Za-z0-9_.:-]+$")

app = FastAPI(title="uptime-bench Uptime Kuma bridge")


class MonitorIn(BaseModel):
    id: str = Field(min_length=1, max_length=120)
    name: Optional[str] = Field(default=None, max_length=160)
    url: str
    interval_seconds: int = Field(default=60, ge=1)
    method: str = "GET"
    keyword: Optional[str] = None
    keyword_check: str = "present"
    headers: Dict[str, str] = Field(default_factory=dict)


def require_auth(authorization: Optional[str]) -> None:
    if not TOKEN:
        raise HTTPException(status_code=500, detail="BRIDGE_TOKEN is not configured")
    if authorization != f"Bearer {TOKEN}":
        raise HTTPException(status_code=401, detail="unauthorized")


def jsonable(value):
    if isinstance(value, dict):
        return {k: jsonable(v) for k, v in value.items()}
    if isinstance(value, list):
        return [jsonable(v) for v in value]
    if hasattr(value, "value"):
        return value.value
    if isinstance(value, datetime):
        return value.astimezone(timezone.utc).isoformat()
    return value


def with_api():
    api = UptimeKumaApi(KUMA_URL, timeout=20)
    last_err = None
    for _ in range(12):
        try:
            api.connect()
            if api.need_setup():
                api.setup(KUMA_USERNAME, KUMA_PASSWORD)
            api.login(KUMA_USERNAME, KUMA_PASSWORD)
            return api
        except Exception as err:
            last_err = err
            try:
                api.disconnect()
            except Exception:
                pass
            time.sleep(5)
    raise HTTPException(status_code=503, detail=f"uptime-kuma unavailable: {last_err}")


def monitor_name(req_id: str, name: Optional[str]) -> str:
    return name or f"uptime-bench:{req_id}"


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/monitors")
def list_monitors(authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    api = with_api()
    try:
        monitors = api.get_monitors()
        return {"monitors": jsonable(monitors)}
    finally:
        api.disconnect()


@app.post("/monitors")
def create_monitor(req: MonitorIn, authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    if not ID_RE.match(req.id):
        raise HTTPException(status_code=400, detail="id contains unsupported characters")
    method = req.method.upper()
    if method not in {"GET", "HEAD"}:
        raise HTTPException(status_code=400, detail="unsupported method")
    if req.keyword and req.keyword_check != "present":
        raise HTTPException(status_code=400, detail="inverted keyword checks are not supported")

    api = with_api()
    try:
        kwargs = {
            "type": MonitorType.KEYWORD if req.keyword else MonitorType.HTTP,
            "name": monitor_name(req.id, req.name),
            "url": req.url,
            "interval": req.interval_seconds,
            "retryInterval": req.interval_seconds,
            "maxretries": 0,
            "method": method,
            "accepted_statuscodes": ["200-299", "300-399"],
        }
        if req.keyword:
            kwargs["keyword"] = req.keyword
        if req.headers:
            kwargs["headers"] = req.headers
        result = api.add_monitor(**kwargs)
        monitor_id = result.get("monitorID") or result.get("monitorId")
        return {"id": req.id, "monitor_id": str(monitor_id), "result": jsonable(result)}
    finally:
        api.disconnect()


@app.delete("/monitors/{monitor_id}")
def delete_monitor(monitor_id: int, authorization: Optional[str] = Header(default=None)):
    require_auth(authorization)
    api = with_api()
    try:
        result = api.delete_monitor(monitor_id)
        return {"monitor_id": str(monitor_id), "result": jsonable(result)}
    finally:
        api.disconnect()


@app.get("/monitors/{monitor_id}/beats")
def beats(
    monitor_id: int,
    authorization: Optional[str] = Header(default=None),
    hours: int = Query(default=3, ge=1, le=168),
):
    require_auth(authorization)
    api = with_api()
    try:
        return {"monitor_id": str(monitor_id), "beats": jsonable(api.get_monitor_beats(monitor_id, hours))}
    finally:
        api.disconnect()
