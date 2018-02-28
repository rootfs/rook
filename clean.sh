#!/bin/bash

kubectl delete -f my/single-cluster.yaml
sleep 5
kubectl delete -f my/rook-operator.yaml

