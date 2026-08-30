FROM rust:1.95-bookworm AS build

WORKDIR /src
COPY agent-runtime/Cargo.toml agent-runtime/Cargo.lock ./
COPY agent-runtime/src ./src
RUN cargo build --locked --release --bins

FROM debian:bookworm-slim AS runtime-base

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        coreutils \
        file \
        findutils \
        git \
        grep \
        gzip \
        jq \
        less \
        procps \
        proot \
        sed \
        tar \
        unzip \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 runtime \
    && useradd --create-home --uid 10001 --gid 10001 runtime \
    && mkdir -p /runtime/workspaces /runtime/skills /runtime/logs \
    && chown -R runtime:runtime /runtime

WORKDIR /runtime
COPY --from=build /src/target/release/agent-runtime /usr/local/bin/agent-runtime
COPY --from=build /src/target/release/agent-runtime-exec /usr/local/bin/agent-runtime-exec
COPY --from=build /src/target/release/fetch /usr/local/bin/fetch
COPY --from=build /src/target/release/agent-runtime-net-probe /usr/local/bin/agent-runtime-net-probe

ENV AGENT_RUNTIME_ADDR=0.0.0.0:8080
ENV AGENT_RUNTIME_WORKSPACE_ROOT=/runtime/workspaces
ENV AGENT_RUNTIME_SKILLS_ROOT=/runtime/skills
ENV AGENT_RUNTIME_TRACE_JSONL=/runtime/logs/runtime-traces.jsonl
ENV AGENT_RUNTIME_BASH_SANDBOX=proot

USER runtime
EXPOSE 8080
CMD ["agent-runtime"]

FROM gcr.io/distroless/cc-debian12:nonroot AS broker

COPY --from=build /src/target/release/agent-fetch-broker /usr/local/bin/agent-fetch-broker

USER 10002:10001
ENTRYPOINT ["/usr/local/bin/agent-fetch-broker"]

FROM runtime-base AS runtime-security-test

LABEL org.csusters.agent-runtime.security-test-only="true"
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl wget python3 nodejs \
    && rm -rf /var/lib/apt/lists/*
ENV AGENT_RUNTIME_SECURITY_TEST_ONLY=1
USER runtime

FROM runtime-base AS runtime
