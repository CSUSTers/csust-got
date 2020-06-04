#!/usr/bin/env bash

export CSUST_BOT_TOKEN=`cat .token`
git pull
docker-compose down
docker-compose build
docker stop $(docker ps -a | grep "Exited" | awk '{print $1 }')
docker rm $(docker ps -a | grep "Exited" | awk '{print $1 }')
docker rmi $(docker images | grep "none" | awk '{print $3}')
docker-compose up -d