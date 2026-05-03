FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir fastapi==0.111.1 uvicorn==0.30.6 uptime-kuma-api-v2==1.0.1

COPY bridge.py /app/bridge.py

EXPOSE 3002

CMD ["uvicorn", "bridge:app", "--host", "0.0.0.0", "--port", "3002"]
