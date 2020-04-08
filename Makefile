.PHONY : get build test fmt deploy

get :
	go get -v .
  
build : get
	go build .

test : 
	go test -v ./...

fmt :
	gofmt .

deploy: get
ldflag = -s -w
cgoflag = 0
output = got
	CGO_ENABLED=$(cgoflag) \
	go build -o $(output) -gcflags "$(ldflag)" . 

