.PHONY : get build test fmt deploy

ldflag = -s -w
cgoflag = 0
buildoutput = got

get :
	go get -v .
  
build : get
	go build .

test : 
	go test -v ./...

fmt :
	gofmt -l -w .

deploy: get
	CGO_ENABLED=$(cgoflag) \
	go build -o $(buildoutput) -ldflags "$(ldflag)" . 

clean:
	rm -f got
