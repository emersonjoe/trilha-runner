#!/bin/sh
set -eu

test "$(id -u)" -eq 0
id trilha-runner >/dev/null 2>&1 || useradd --system --home /var/lib/trilha-runner --shell /usr/sbin/nologin trilha-runner
install -d -o root -g trilha-runner -m 0750 /etc/trilha-runner
install -d -o trilha-runner -g trilha-runner -m 0700 /var/lib/trilha-runner/workspaces
install -o root -g root -m 0755 trilha-runner /usr/local/bin/trilha-runner
install -o root -g root -m 0644 deploy/eoslab/trilha-runner.service /etc/systemd/system/trilha-runner.service
test -f /etc/trilha-runner/worker.env || install -o root -g trilha-runner -m 0640 deploy/eoslab/worker.env.example /etc/trilha-runner/worker.env
test -f /etc/trilha-runner/delivery.json || install -o root -g trilha-runner -m 0640 deploy/eoslab/delivery.json.example /etc/trilha-runner/delivery.json
systemctl daemon-reload
systemctl enable trilha-runner.service
