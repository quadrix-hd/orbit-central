FROM python:3.13-slim-trixie
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 ORBIT_DATA_DIR=/data PORT=8080 ORBIT_NOVNC_ROOT=/opt/orbit-novnc
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends novnc && cp -aL /usr/share/novnc /opt/orbit-novnc && rm -rf /var/lib/apt/lists/*
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt && useradd --uid 10001 --create-home orbit && mkdir /data && chown orbit:orbit /data
COPY --chown=orbit:orbit backend backend
COPY --chown=orbit:orbit dist dist
COPY --chown=orbit:orbit scripts/backup.py scripts/reset-access.py scripts/restore.py scripts/
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/api/health',timeout=2)"]
CMD ["python", "backend/server.py"]
