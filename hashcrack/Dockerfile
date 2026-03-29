FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      git \
      make \
      gcc \
      g++ \
      pkg-config \
      libssl-dev \
      zlib1g-dev \
      python3 && \
    rm -rf /var/lib/apt/lists/*

RUN git clone --depth=1 https://github.com/openwall/john.git /opt/john && \
    cd /opt/john/src && \
    ./configure && \
    make -s -j"$(nproc)"

WORKDIR /app

COPY hashcrack/server.py /app/server.py
COPY hashcrack/fasttrack.txt /app/fasttrack.txt

ENV PORT=9021

CMD ["python3", "/app/server.py"]
