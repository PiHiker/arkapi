FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      build-essential \
      pkg-config \
      curl && \
    rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir libretranslate

EXPOSE 5000

CMD ["libretranslate", "--host", "0.0.0.0", "--port", "5000", "--threads", "1", "--load-only", "en,es,fr,de,it,pt"]
