# Sažetak ponovljenih merenja

## exp-01-broker-pod-failure  (3 prolaza)

| Veličina | Medijana | Najmanje | Najviše | Raspon |
| :- | -: | -: | -: | -: |
| pokušaja objave | 296 | 296 | 296 | — |
| uspešnih | 296 | 296 | 296 | — |
| neuspešnih | 0 | 0 | 0 | — |
| prozor nedostupnosti | 200 | 200 | 204 | 4 |
| pod ponovo Ready | 51227 | 50919 | 51308 | 389 |

## exp-02-pdb-quorum-guard  (3 prolaza)

| Veličina | Medijana | Najmanje | Najviše | Raspon |
| :- | -: | -: | -: | -: |
| prva eviction | 201 | 201 | 201 | — |
| druga eviction | 429 | 429 | 429 | — |

## exp-03-node-failure  (3 prolaza)

| Veličina | Medijana | Najmanje | Najviše | Raspon |
| :- | -: | -: | -: | -: |
| pokušaja objave | 196 | 180 | 216 | 36 |
| uspešnih | 185 | 169 | 202 | 33 |
| neuspešnih | 11 | 11 | 14 | 3 |
| najduži neprekidni prekid | 50021 | 31088 | 50025 | 18937 |
| stopa otkaza | 8.4 | 7.5 | 8.5 | 1.0 |
| čvor označen NotReady | 41 | 37 | 41 | 4 |

## exp-04-network-partition  (3 prolaza)

| Veličina | Medijana | Najmanje | Najviše | Raspon |
| :- | -: | -: | -: | -: |
| pokušaja objave | 89 | 89 | 106 | 17 |
| uspešnih | 74 | 74 | 91 | 17 |
| neuspešnih | 15 | 15 | 15 | — |
| najduži neprekidni prekid | 10008 | 5003 | 10008 | 5005 |
| pokušaja posle podele | 40 | 39 | 57 | 18 |
| neuspešnih posle podele | 15 | 15 | 15 | — |
| Raft članova tokom otkaza | 3 | 3 | 3 | — |

## exp-05-network-delay  (3 prolaza)

| Veličina | Medijana | Najmanje | Najviše | Raspon |
| :- | -: | -: | -: | -: |
| pokušaja tokom kašnjenja | 72 | 72 | 76 | 4 |
| Raft članova tokom otkaza | 3 | 3 | 3 | — |
| liderstvo nepromenjeno | isto u svim prolazima | | | |
