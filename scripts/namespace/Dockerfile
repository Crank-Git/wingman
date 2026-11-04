FROM ubuntu:noble-20261024.1@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
RUN apt-get update && apt-get install -y --no-install-recommends iproute2=6.1.0-1ubuntu6.4 && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["bash", "/repo/scripts/namespace/entrypoint.sh"]
