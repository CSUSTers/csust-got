#!/usr/bin/env bash

export CSUST_BOT_TOKEN=`cat .token`
git pull
docker-compose down
docker-compose build
docker-compose up -d