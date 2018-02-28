#!/bin/bash
set -e
mkdir -p my
go build -i -o my/rook cmd/rook/* 
go build -i -o my/rookflex cmd/rookflex/main.go
cd my
docker build -t localhost:5000/rook .
docker push localhost:5000/rook
