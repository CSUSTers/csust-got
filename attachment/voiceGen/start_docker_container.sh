#!/bin/bash
app="voice_gen"
docker build -t ${app} .
docker run -d -p 37332:8000 voice_gen