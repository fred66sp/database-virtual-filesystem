#!/usr/bin/env python3
"""content_hash canónico de ACM — implementa DESIGN §5.2.

Uso: echo '{"kind":"decision","title":...}' | content-hash.py observations
Salida: sha256:<64 hex> (o error JSON por stderr, exit 1).
Los campos del array canónico por tabla están en CAMPOS, según DESIGN §5.2.
"""
import hashlib
import json
import sys
import unicodedata

CAMPOS = {
    "observations": ["kind", "title", "body", "rationale", "alternatives",
                     "consequences", "topic_key"],
    "checkpoints": ["goal", "state", "next_steps", "files", "open_questions"],
    "preferences": ["pref_key", "value", "rationale"],
}


def normaliza(texto: str) -> str:
    # DESIGN §5.2.1: NFC, recorte de extremos, whitespace interno colapsado
    return " ".join(unicodedata.normalize("NFC", texto or "").split())


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] not in CAMPOS:
        print(f'{{"error":"tabla requerida: {", ".join(CAMPOS)}"}}',
              file=sys.stderr)
        return 1
    try:
        datos = json.load(sys.stdin)
    except json.JSONDecodeError as e:
        print(f'{{"error":"stdin no es JSON válido: {e}"}}', file=sys.stderr)
        return 1
    # DESIGN §5.2.2: array JSON canónico, orden fijo, sin espacios, ausentes ""
    # Los campos dict/list se serializan ya compactos y entran como texto.
    canon = []
    for campo in CAMPOS[sys.argv[1]]:
        valor = datos.get(campo)
        # §5.2: los campos ausentes (o null) se representan como cadena vacía
        if valor is None:
            valor = ""
        if isinstance(valor, (dict, list)):
            canon.append(json.dumps(valor, ensure_ascii=False,
                                    separators=(",", ":")))
        else:
            canon.append(normaliza(str(valor)))
    arreglo = json.dumps(canon, ensure_ascii=False, separators=(",", ":"))
    # DESIGN §5.2.3-4: SHA-256 sobre UTF-8, hex minúsculas
    print("sha256:" + hashlib.sha256(arreglo.encode("utf-8")).hexdigest())
    return 0


if __name__ == "__main__":
    sys.exit(main())
