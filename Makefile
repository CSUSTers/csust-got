.PHONY : get build

get :
    go get -v .
  
build : get
    go build .
