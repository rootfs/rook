#!/bin/bash
rm -rf /var/lib/rook 
kubectl create -f my/rook-operator.yaml
sleep 5
kubectl create -f my/single-cluster.yaml
