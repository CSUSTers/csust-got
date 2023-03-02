#!/bin/bash
app="voice_gen"
tag="voice_gen"

docker build -t ${tag} .

# stop and remove running docker container
if docker ps -a | grep -q $app; then
  docker stop $app
  
  if [ $? -eq 0 ]; then
    docker rm $container_name
  else
    echo "cannot stop container, exit."
    exit 255
  fi
fi

docker run -d -p 37332:8000 --name $app $tag
