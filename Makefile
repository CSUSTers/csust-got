.PHONY : get build test fmt

get :
	go get -v .
  
build : get
	go build .

test : 
	go test -v ./...

fmt :
	gofmt .
