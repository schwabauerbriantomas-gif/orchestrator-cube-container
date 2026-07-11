#!/usr/bin/env bash

echo "sleep..."
sleep 10
echo "Stopping..."
BIN="cubemaster"

PROCESS_PID=`pidof ${BIN}`

echo `date`": kill pid:${PROCESS_PID}"

ret=0
for PID in ${PROCESS_PID}
do
    cmd="kill -s term $PID"
    echo ${cmd}
    eval ${cmd}
    let ret=ret+$?
done
echo `date`": kill ret:$ret"


# Check graceful process shutdown
timer=15
while [[ true ]]; do
pidof ${BIN}
    if [[ "$?" == 0 ]]; then
        echo "wait service stop."
	    sleep 1
	    let timer=timer-1
    else
        echo -e "\033[32m service stopped. \033[0m"
		exit 0
		# 5s buffer for debugging (log/process inspection)
		sleep 5
		break
    fi

	echo ${timer}
	if [[ ${timer} -le 0 ]]; then
	    echo -e "\033[47;31m can't stop.\033[0m"
		exit 1
	fi
done

