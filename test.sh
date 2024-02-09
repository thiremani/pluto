#!/bin/bash

cd lexer && go test -race
cd ..
cd parser && go test -race