# __IMPORTENT__ 
# requires docker 17.3 and above

# build
FROM golang:alpine AS buildenv

WORKDIR /go/src/app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o got -ldflags "-s -w" .

# deploy image
FROM alpine

RUN apk add tzdata

WORKDIR /app
COPY --from=buildenv /go/src/app/got .

CMD ["./got"]
