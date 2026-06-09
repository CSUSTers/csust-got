FROM rust:1.95-bookworm AS build

WORKDIR /src
COPY agent-runtime/Cargo.toml agent-runtime/Cargo.lock* ./
COPY agent-runtime/src ./src
RUN cargo build --release

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates coreutils findutils grep proot sed \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -u 10001 runtime \
    && mkdir -p /runtime/workspaces /runtime/skills /runtime/logs \
    && chown -R runtime:runtime /runtime

WORKDIR /runtime
COPY --from=build /src/target/release/agent-runtime /usr/local/bin/agent-runtime

ENV AGENT_RUNTIME_ADDR=0.0.0.0:8080
ENV AGENT_RUNTIME_WORKSPACE_ROOT=/runtime/workspaces
ENV AGENT_RUNTIME_SKILLS_ROOT=/runtime/skills
ENV AGENT_RUNTIME_TRACE_JSONL=/runtime/logs/runtime-traces.jsonl
ENV AGENT_RUNTIME_BASH_SANDBOX=proot

USER runtime
EXPOSE 8080
CMD ["agent-runtime"]
