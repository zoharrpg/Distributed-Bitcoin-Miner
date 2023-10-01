#!/bin/zsh
# check if the file checkpoint.out exits
if [ -f "checkpoint.out" ]; then
    rm checkpoint.out
fi
touch checkpoint.out
# loop to execute the basic test cases
for i in {1..9}
do
    go test -run TestBasic$i | tee -a checkpoint.out
done
# loop to execute the out of order message test cases
for i in {1..3}
do
    go test -run TestOutOfOrderMsg$i | tee -a checkpoint.out
done
go test -run TestBasicISN | tee -a checkpoint.out
