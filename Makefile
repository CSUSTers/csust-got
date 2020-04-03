.PHONY : get build test

get :
	go get -v .
  
build : get
	go build .

test : 
	go test -v ./...
