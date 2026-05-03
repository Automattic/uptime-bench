FROM python:3.12-alpine

WORKDIR /app

RUN pip install --no-cache-dir fastapi==0.111.1 pyyaml==6.0.2 requests==2.32.3 uvicorn==0.30.6

COPY bridge.py /app/bridge.py

EXPOSE 8081

CMD ["uvicorn", "bridge:app", "--host", "0.0.0.0", "--port", "8081"]
