# build
FROM --platform=$BUILDPLATFORM golang:1.20-alpine AS buildenv
RUN set -eux && sed -i 's/dl-cdn.alpinelinux.org/mirrors.ustc.edu.cn/g' /etc/apk/repositories
ENV GOPROXY="https://goproxy.io"
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

RUN apk add tzdata

WORKDIR /app
COPY --from=buildenv /go/src/app/got .
COPY --from=buildenv /go/src/app/config.yaml .

CMD ["./got"]
