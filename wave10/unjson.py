import json, sys
d = json.load(open(sys.argv[1]))
open(sys.argv[2], 'w').write(d['logs_content'].lstrip('﻿'))
