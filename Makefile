DIAGRAMS := $(patsubst %.dot,%.png,$(wildcard docs/*.dot))

default: init diagrams

init:
	find . -name "*.sh" -exec chmod +x {} \;

diagrams: $(DIAGRAMS)

%.png: %.dot
	dot -Tpng $< -o $@

.PHONY: init diagrams
