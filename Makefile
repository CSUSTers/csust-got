.PHONY: get build test fmt deploy run clean

PROJECT := csust-got
VERSION := $(shell git rev-parse --short HEAD)
BRANCH := $(shell git branch --show-current)
BUILDTIME := $(shell TZ="Asia/Shanghai" date '+%Y/%m/%d-%H:%M:%S')

FLAGPKG = $(PROJECT)/base
LDFLAGS = -s -w
LDFLAGS += -X $(FLAGPKG).version=$(VERSION)
LDFLAGS += -X $(FLAGPKG).branch=$(BRANCH)
LDFLAGS += -X $(FLAGPKG).buildTime=$(BUILDTIME)

CGOFLAG = 0
OUTPUT = got

get:
	go get -v .
  
build: get
	go build .

test: 
	go test -v ./...

fmt:
	gofmt -l -w .

deploy: get
	CGO_ENABLED=$(CGOFLAG) \
	go build -o $(OUTPUT) -ldflags "$(LDFLAGS)" . 

run: deploy
	./$(OUTPUT)

clean:
	rm -f $(OUTPUT)
