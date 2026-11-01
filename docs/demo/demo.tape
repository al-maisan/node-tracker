# Demo GIF for the README; regenerate with docs/demo/record.sh
Output demo.gif
Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 500
Set Padding 16
Set TypingSpeed 35ms
Set Theme "Dracula"

Hide
Type "export PATH=$PWD:$PATH PS1='$ ' && mkdir -p ~/.bitcoin ~/.config/node-tracker && cp config.yaml ~/.config/node-tracker/" Enter
Type "echo __cookie__:demo >~/.bitcoin/.cookie && echo 123456:demo >~/.config/node-tracker/telegram-token && chmod 600 ~/.bitcoin/.cookie ~/.config/node-tracker/telegram-token" Enter
Type "fake >telegram.log 2>&1 & sleep 1; export SSL_CERT_FILE=$PWD/cert.pem; clear" Enter
Sleep 1.5s
Show

Type "cat ~/.config/node-tracker/config.yaml" Enter
Sleep 4s
Type "clear" Enter
Type "node-tracker check" Enter
Sleep 3s
Type "node-tracker test-notify" Enter
Sleep 2.5s
Type "node-tracker once    # lagging, but alert.for is 2: no alert yet" Enter
Sleep 2s
Type "node-tracker once    # 2nd lagging tick in a row: alert sent" Enter
Sleep 5s
