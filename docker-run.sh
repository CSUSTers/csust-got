#!/usr/bin/env bash

CSUST_BOT_TOKEN=`cat .token`
git pull
docker-compose down
docker-compose build
docker-compose up -d