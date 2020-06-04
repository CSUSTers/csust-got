.PHONY : get build test fmt deploy

get :
	go get -v .
  
build : get
	go build .

test : 
	go test -v ./...

fmt :
	gofmt -l -w .

ldflag = -s -w
cgoflag = 0
output = got
deploy: get
	CGO_ENABLED=$(cgoflag) \
	go build -o $(output) -ldflags "$(ldflag)" . 

clean:
	rm -f csust-got got
