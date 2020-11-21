# __IMPORTENT__ 
# requires docker 17.3 and above

# build
FROM golang AS buildenv

WORKDIR /go/src/app
COPY . .
RUN make deploy

# deploy image
FROM alpine

WORKDIR /app
COPY --from=buildenv /go/src/app/got .

CMD ["./got"]
