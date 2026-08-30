FROM python:3.13-slim

LABEL org.csusters.agent-runtime.security-test-only="true"

RUN groupadd --gid 10003 fixture \
    && useradd --no-create-home --uid 10003 --gid 10003 fixture

WORKDIR /fixture
COPY --chown=10003:10003 fixture_server.py /fixture/fixture_server.py

ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

USER 10003:10003
EXPOSE 8080/tcp 5353/tcp 5353/udp
HEALTHCHECK --interval=2s --timeout=1s --retries=15 \
  CMD ["python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/__state', timeout=0.5).read()"]
ENTRYPOINT ["python3", "/fixture/fixture_server.py"]
