# build
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS buildenv
ARG TARGETARCH

RUN apk add make git tzdata

ARG BRANCH
ARG TAG
ARG RELEASE

ENV BRANCH=$BRANCH
ENV TAG=$TAG
ENV GOARCH=$TARGETARCH

WORKDIR /go/src/app
COPY . .
RUN make deploy


# deploy image
FROM --platform=$BUILDPLATFORM alpine

RUN apk add --no-cache tzdata
COPY --from=hugefiver/ffmpeg:7.0.1-2 /ffmpeg /usr/local/bin/ffmpeg
# COPY --from=hugefiver/ffmpeg:7.0.1 /ffprobe /usr/local/bin/ffprobe

WORKDIR /app
COPY --from=buildenv /go/src/app/got .
COPY --from=buildenv /go/src/app/config.yaml .
COPY --from=buildenv /go/src/app/dict/dictionary.txt .
COPY --from=buildenv /go/src/app/dict/stop_words.txt .


CMD ["./got"]
