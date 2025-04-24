# build
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS buildenv
ARG TARGETARCH

RUN apk add make git tzdata

ARG BRANCH
ARG TAG
ARG RELEASE

WORKDIR /go/src/app
COPY . .
RUN make deps

ENV BRANCH=$BRANCH
ENV TAG=$TAG
ENV GOARCH=$TARGETARCH

RUN make deploy


# deploy image
FROM --platform=$BUILDPLATFORM alpine

RUN apk add --no-cache tzdata
COPY --from=ghcr.io/hugefiver/static-ffmpeg:latest /ffmpeg /usr/local/bin/ffmpeg

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
# preinstall mcp tools
RUN for package in mcp-server-time mcp-searxng mcp-server-fetch; do \
    uv tool install --no-cache "$package"; done

WORKDIR /app
COPY --from=buildenv /go/src/app/got .
COPY --from=buildenv /go/src/app/config.yaml .
COPY --from=buildenv /go/src/app/dict/dictionary.txt .
COPY --from=buildenv /go/src/app/dict/stop_words.txt .


CMD ["./got"]
