# Visoko dostupan RabbitMQ na Kubernetes-u, sa namenskim Go operatorom

Ovo je praktični deo diplomskog rada. Sastoji se iz dve faze.

**Faza 1** postavlja RabbitMQ klaster od tri čvora na Kubernetes platformi. Klaster koristi *quorum* redove — redove koji svoje poruke replikuju Raft algoritmom konsenzusa. RabbitMQ je uklonio klasične *mirrored* redove, pa su *quorum* redovi današnji standard za trajnost poruke.

**Faza 2** dodaje namenski Kubernetes operator, pisan u jeziku Go pomoću okvira Kubebuilder. Operator čita jedan *Custom Resource* i na osnovu njega obezbeđuje kompletno okruženje za razmenu poruka jednog mikroservisa: *vhost*, razmenu, *quorum* redove sa pripadajućim *dead letter* redovima, namenskog korisnika i Kubernetes tajnu sa pristupnim nizom.

Sam rad nalazi se u `docs/thesis/diplomski.md`.

## Preduslovi

Na svojoj mašini treba da imate:

- Docker Desktop ili Docker Engine, sa **najmanje 12 GB** dodeljene memorije
- `kind`, verzija 0.33 ili novija
- `kubectl`
- `helm`
- Go 1.23 ili noviji i `kubebuilder` — potrebni su samo za Fazu 2

## Pokretanje

```bash
./setup-demo.sh
```

Prvo pokretanje traje oko deset minuta. Skripta je idempotentna: ako neki korak otkaže, jednostavno je pokrenite ponovo i nastaviće odande gde je stala.

Za brisanje i ponovnu izgradnju:

```bash
./setup-demo.sh --clean
./setup-demo.sh
```

Po završetku skripta ispisuje pristupne podatke. *Management* korisnički interfejs otvara se na http://localhost:15672, sa nalogom `admin` / `admin`.

Cluster Operator uz to generiše sopstvenog korisnika za svoje provere ispravnosti. **Nemojte ga brisati** — skripta njegovo ime ispisuje na kraju.

## Šta skripta gradi

| Korak | Komponenta | Čemu služi |
|---|---|---|
| 1 | `kind` klaster | Jedan kontrolni i tri radna čvora |
| 2 | Slika brokera | Učitava `rabbitmq:4.3.4-management` na svaki radni čvor |
| 3 | cert-manager | Izdaje TLS sertifikat za *webhook* operatora |
| 4 | RabbitMQ Cluster Operator | Upravlja *StatefulSet*-om brokera |
| 5 | `RabbitmqCluster` | Sam klaster od tri replike |
| 6 | Provera rasporeda | Potvrđuje da je po jedan broker na svakom čvoru |
| 7 | Administratorski nalog | Kreira `admin` / `admin` |
| 8 | Chaos Mesh | Ubrizgava otkaze za eksperimente |
| 9 | Autorski operator | Gradi i raspoređuje Fazu 2. Preskače se ako `operator/` ne postoji. |

## Zašto je konfiguracija ovakva

**Tri radna čvora, a ne jedan.** Raft traži većinu. Tri replike preživljavaju gubitak jedne, jer su dva glasa i dalje većina. Klaster sa jednim čvorom to ne može da pokaže.

**Anti-affinity je obavezan.** Kubernetes raspoređivač sme da smesti dva brokera na isti čvor. Taj čvor tada drži dva od tri Raft glasa, pa njegov ispad uništava većinu i zaustavlja red. Pravilo u `infra/rabbitmq/rabbitmq-ha.yaml` to sprečava, a korak 6 skripte proverava stvarni ishod i prekida izvršavanje ako pravilo nije dalo efekat.

**Memorija koju Docker dobija.** Skripta čita `docker info` i upozorava ispod 10 GB. Na Linux-u je to fizička memorija računara; na Docker Desktop-u je ograničenje virtuelne mašine, i to je broj koji treba podići.

**Slika brokera se unapred učitava.** Kada tri čvora istovremeno povlače istu sliku, nailazi se na ograničenje broja zahteva i na delimično zapisane slojeve, posle čega čvor ostaje u stanju `ImagePullBackOff`. Skripta sliku povlači jednom i uvozi je na svaki čvor, pa demonstracija radi i bez interneta.

**cert-manager je zavisnost, ne dodatak.** RabbitMQ Cluster Operator 2.23 svoje *admission webhook*-e izlaže preko TLS-a i sertifikat uzima od cert-manager-a. Bez njega primena manifesta operatora ne uspeva.

## Provera da sve radi

```bash
./verify-demo.sh
```

Skripta primenjuje jedan profil i proverava šta broker zaista sadrži: četiri *quorum* reda sa po tri Raft člana, namenskog korisnika sa uskim ovlašćenjima, tajnu sa pristupnim nizom, i čišćenje posle brisanja. Ispisuje svaku proveru pojedinačno, pa izlaz služi i kao dokaz.

Testovi operatora:

```bash
cd operator && make test
```

## Ponavljanje merenja

Poglavlje 6 rada navodi medijanu od tri prolaza po eksperimentu, sa rasponom. Merenja se ponavljaju ovako:

```bash
./experiments/run-repeated.sh 3
```

Skripta upisuje po jednu datoteku za svaki prolaz u `experiments/results/runs/`, a zbirni pregled sa medijanom i rasponom u `experiments/results/SAZETAK.md`. Traje oko pola sata i između prolaza čeka da se sva tri brokera vrate u ispravno stanje, kako sledeći prolaz ne bi merio rep prethodnog.

## Izazivanje otkaza

Primenite eksperiment, pa pratite zapise brokera i *management* interfejs.

```bash
# Odseci jedan broker od preostala dva. Preostala dva zadržavaju većinu.
kubectl --context kind-diplomski-ha apply -f infra/chaos/network-partition.yaml

# Dodaj 100 ms kašnjenja između svih brokera. Raft usporava, ali ne staje.
kubectl --context kind-diplomski-ha apply -f infra/chaos/network-delay.yaml

# Ubij jedan broker pod. StatefulSet ga vraća, a Raft ga sustiže.
kubectl --context kind-diplomski-ha apply -f infra/chaos/pod-kill-leader.yaml
```

Gubitak cele mašine simulira se zaustavljanjem kontejnera radnog čvora:

```bash
docker stop diplomski-ha-worker2
docker start diplomski-ha-worker2
```

## Sadržaj repozitorijuma

```
docs/thesis/
  diplomski.md                   Rad. Ovo je izvor; sve ostalo u toj fascikli
                                 generiše se iz njega.
  diplomski.docx                 Generisano. Ne menjati rukom.
  slike/*.mmd                    Mermaid izvori dijagrama
  md-to-docx.js                  Markdown u .docx, sa uputstvom u README-u
infra/
  kind/kind-ha-config.yaml       Topologija klastera i preslikavanje portova
  operators/cert-manager.yaml    Fiksirana verzija cert-manager-a
  operators/cluster-operator.yml Fiksirana verzija Cluster Operator-a
  rabbitmq/rabbitmq-ha.yaml      RabbitmqCluster i PodDisruptionBudget
  chaos/                         Chaos Mesh eksperimenti
operator/                        Faza 2. Autorski operator u jeziku Go.
  api/v1alpha1/                  Definicija MicroserviceMessagingProfile
  internal/rabbitmq/             Klijent prema management API-ju
  internal/controller/           Reconcile petlja
experiments/
  exp-01..05-*.sh                Po jedna skripta za svaki scenario otkaza
  run-repeated.sh                Pokreće svih pet eksperimenata N puta
  aggregate.py                   Svodi prolaze na medijanu, minimum i maksimum
  plot-results.py                Crta Sliku 6.1 iz sirovih uzoraka
  results/                       Izmereni izlazi, uključujući svih 15 prolaza
setup-demo.sh                    Podiže celo okruženje
verify-demo.sh                   18 provera nad živim klasterom
```

## Portovi

`kind` konfiguracija preslikava dva porta sa host mašine na `NodePort` servise. Demonstracija time ne zavisi od `kubectl port-forward`, koji prekida vezu svaki put kada pod nestane — a upravo to se u eksperimentima namerno izaziva.

| Port na host mašini | NodePort | Usluga |
|---|---|---|
| 15672 | 30672 | RabbitMQ *management* interfejs |
| 5672 | 30567 | AMQP |
