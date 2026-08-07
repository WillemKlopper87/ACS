#!/bin/bash
# Tails all three service logs together, prefixed by source — handy
# when watching for a device's first Inform (deployment-testing-
# onboarding-guide.md §6).
LOG_DIR="$HOME/acs-logs"
tail -f "$LOG_DIR/acs.log" "$LOG_DIR/api.log" "$LOG_DIR/frontend.log"
