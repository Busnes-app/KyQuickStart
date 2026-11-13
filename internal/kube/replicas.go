package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// A replicated app (KyIdentity with the plan's replicas) is three workloads in its namespace:
// "app", the one pod that keeps the data claim and runs setup; "app-replica", the others, with
// no claim; and "database", one Postgres pod, unless the plan names the operator's own.
const (
	replicaName    = "app-replica"
	databaseName   = "database"
	keysSecret     = "kyq-replica-keys"
	databaseSecret = "kyq-database"
	servesLabel    = "ky.serves" // on every pod the app's Service sends to
	keysDir        = "/run/kyq-keys"
	databaseDir    = "/run/kyq-database"
	databasePort   = 5432
	databaseUser   = "kyidentity"
	// postgresUID is the postgres user of the official Alpine image.
	postgresUID = 70
	// bootstrapKey is the first administrator's password, which a replica cannot generate.
	bootstrapKey = "bootstrap-admin-pass"
	passwordKey  = "password"
	// superuserKey is the password of Postgres's own superuser, which nothing connects as.
	superuserKey = "superuser-password"
)

// databaseCommand writes the first start's init script, then starts Postgres as the image
// does. The script gives the app a role that owns its database and nothing else, and keeps a
// failing statement, which would carry the password, out of the server log.
const databaseCommand = `cat > /docker-entrypoint-initdb.d/10-app.sql <<'SQL'
SET log_min_error_statement = panic;
\set pw ` + "`cat /run/secrets/" + passwordKey + "`" + `
CREATE ROLE ` + databaseUser + ` LOGIN PASSWORD :'pw';
CREATE DATABASE ` + databaseUser + ` OWNER ` + databaseUser + ` ENCODING 'UTF8' LOCALE 'C' TEMPLATE template0;
SQL
exec docker-entrypoint.sh postgres`

// replicaKeys are the four keys every replica must share: the variable that names each, and
// its file in a single instance's data directory, which is also its key in the Secret.
var replicaKeys = []struct{ env, file string }{
	{"KYIDENTITY_SECRET_KEY_FILE", "secret.key"},
	{"KYIDENTITY_ENCRYPTION_KEY_FILE", "encryption.key"},
	{"KYIDENTITY_RSA_KEY_PATH", "jwt_rs256.key"},
	{"KYIDENTITY_VAPID_KEY_PATH", "push-vapid-private.key"},
}

var databaseResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}

// replicated reports whether the plan runs the app as replicas.
func (a App) replicated() bool {
	k := a.Catalog.Kubernetes
	return k != nil && k.Replicas != nil && k.ReplicaCount > 1
}

// ownDatabase reports whether the installer deploys the app's Postgres.
func (a App) ownDatabase() bool {
	return a.replicated() && a.Catalog.Kubernetes.DatabaseSecret == ""
}

func (a App) databaseSecretName() string {
	if s := a.Catalog.Kubernetes.DatabaseSecret; s != "" {
		return s
	}
	return databaseSecret
}

func replicaPart(app string) string  { return app + "-replica" }
func databasePart(app string) string { return app + "-database" }

// replicate turns the rendered single instance into the replicated app.
func replicate(o *objects, a App, meta func(string) metav1.ObjectMeta) {
	app, k := a.Name, a.Catalog.Kubernetes
	serves := map[string]string{servesLabel: app}
	spec := &o.Deployment.Spec.Template.Spec
	c := &spec.Containers[0]
	c.Env = append(c.Env,
		corev1.EnvVar{Name: "KYIDENTITY_REPLICATED", Value: "true"},
		corev1.EnvVar{Name: "KYIDENTITY_DATABASE_URL_FILE", Value: databaseDir + "/" + stack.DatabaseSecretKey},
		// The product reads this one from its environment only.
		corev1.EnvVar{Name: "BOOTSTRAP_ADMIN_PASS", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: keysSecret}, Key: bootstrapKey,
		}}})
	if k.RecoveryKeyID != "" {
		c.Env = append(c.Env, corev1.EnvVar{Name: "KYIDENTITY_RECOVERY_KEY_ID", Value: k.RecoveryKeyID})
	}
	var keyFiles []corev1.KeyToPath
	for _, key := range replicaKeys {
		c.Env = append(c.Env, corev1.EnvVar{Name: key.env, Value: keysDir + "/" + key.file})
		keyFiles = append(keyFiles, corev1.KeyToPath{Key: key.file, Path: key.file})
	}
	slices.SortFunc(c.Env, func(x, y corev1.EnvVar) int { return strings.Compare(x.Name, y.Name) })
	secret := func(name, secret string, items []corev1.KeyToPath) {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secret, DefaultMode: ptr.To(int32(0o440)), Items: items},
		}})
	}
	secret("replica-keys", keysSecret, keyFiles)
	secret("database-url", a.databaseSecretName(), []corev1.KeyToPath{{Key: stack.DatabaseSecretKey, Path: stack.DatabaseSecretKey}})
	c.VolumeMounts = append(c.VolumeMounts,
		corev1.VolumeMount{Name: "replica-keys", MountPath: keysDir, ReadOnly: true},
		corev1.VolumeMount{Name: "database-url", MountPath: databaseDir, ReadOnly: true})
	o.Deployment.Spec.Template.Labels[servesLabel] = app
	o.Service.Spec.Selector = serves

	// The other replicas: the same pod with scratch in place of every claim, since nothing a
	// replica keeps in its data directory has to outlive it. Replaced one at a time, each new
	// pod ready before an old one stops.
	r := o.Deployment.DeepCopy()
	r.ObjectMeta = meta(replicaName)
	r.Spec.Replicas = ptr.To(int32(k.ReplicaCount - 1))
	r.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector(replicaPart(app))}
	r.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{
		MaxSurge: ptr.To(intstr.FromInt32(1)), MaxUnavailable: ptr.To(intstr.FromInt32(0)),
	}}
	r.Spec.Template.Labels[nameLabel] = replicaPart(app)
	for i, v := range r.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			r.Spec.Template.Spec.Volumes[i].VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
		}
	}
	o.Replica = r

	if !a.ownDatabase() {
		return
	}
	database(o, a, meta)
}

// database adds the installer's Postgres: one pod on its own claim, reachable only from the
// app's pods and unable to call out. The database is created UTF8 with the C locale, which
// KyIdentity checks at startup.
func database(o *objects, a App, meta func(string) metav1.ObjectMeta) {
	app, c, k := a.Name, a.Catalog, a.Catalog.Kubernetes
	part := databasePart(app)
	claim := "vol-" + databaseName
	restricted := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(int64(postgresUID)),
		RunAsGroup:               ptr.To(int64(postgresUID)),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	listening := corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(databasePort)}}
	var class *string
	if a.StorageClass != "" {
		class = &a.StorageClass
	}
	o.Claims = append(o.Claims, &corev1.PersistentVolumeClaim{
		ObjectMeta: meta(claim),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(k.Replicas.DatabaseSize)}},
			StorageClassName: class,
		},
	})
	labels := podLabels(part, a.ReleaseSet)
	o.Database = &appsv1.Deployment{
		ObjectMeta: meta(databaseName),
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selector(part)},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					Affinity:                     spread,
					Tolerations:                  leaveDeadNode,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:        ptr.To(true),
						FSGroup:             ptr.To(int64(postgresUID)),
						FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
						SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					InitContainers: []corev1.Container{{
						Name:            "private-volumes",
						Image:           CredentialGeneratorImage,
						Command:         []string{"/bin/sh", "-ec", privateScript, "private-volumes", "/volumes/" + databaseName + "/" + privateDir},
						Resources:       corev1.ResourceRequirements{Requests: initResources, Limits: initResources},
						SecurityContext: restricted,
						VolumeMounts:    []corev1.VolumeMount{{Name: claim, MountPath: "/volumes/" + databaseName}},
					}},
					Containers: []corev1.Container{{
						Name:    databaseName,
						Image:   c.Images[k.Replicas.DatabaseImage],
						Command: []string{"/bin/sh", "-ec", databaseCommand},
						Env: []corev1.EnvVar{
							{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"},
							{Name: "POSTGRES_INITDB_ARGS", Value: "--encoding=UTF8 --locale=C"},
							{Name: "POSTGRES_PASSWORD_FILE", Value: "/run/secrets/" + superuserKey},
						},
						Ports:           []corev1.ContainerPort{{ContainerPort: databasePort}},
						Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: databaseResources[corev1.ResourceMemory]}, Limits: databaseResources},
						SecurityContext: restricted,
						ReadinessProbe: &corev1.Probe{PeriodSeconds: 5, ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
							Command: []string{"pg_isready", "-h", "127.0.0.1", "-U", databaseUser, "-d", databaseUser},
						}}},
						StartupProbe:  &corev1.Probe{ProbeHandler: listening, PeriodSeconds: 10, FailureThreshold: 180},
						LivenessProbe: &corev1.Probe{ProbeHandler: listening, PeriodSeconds: 10, FailureThreshold: 6},
						VolumeMounts: []corev1.VolumeMount{
							{Name: claim, MountPath: "/var/lib/postgresql/data", SubPath: privateDir},
							{Name: "tmp", MountPath: "/tmp"},
							{Name: "run", MountPath: "/var/run/postgresql"},
							{Name: "initdb", MountPath: "/docker-entrypoint-initdb.d"},
							{Name: "password", MountPath: "/run/secrets", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: claim, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "initdb", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "password", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: databaseSecret, DefaultMode: ptr.To(int32(0o440)),
							Items: []corev1.KeyToPath{{Key: passwordKey, Path: passwordKey}, {Key: superuserKey, Path: superuserKey}},
						}}},
					},
				},
			},
		},
	}
	port := []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(databasePort))}}
	o.DatabaseService = &corev1.Service{ObjectMeta: meta(databaseName), Spec: corev1.ServiceSpec{
		Selector: selector(part),
		Ports:    []corev1.ServicePort{{Name: "postgres", Port: databasePort, TargetPort: intstr.FromInt32(databasePort)}},
	}}
	// Beside the namespace's default deny: only the app's pods reach Postgres, and it reaches
	// nothing.
	o.DatabasePolicy = &networkingv1.NetworkPolicy{ObjectMeta: meta(databaseName), Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: selector(part)},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{Ports: port, From: []networkingv1.NetworkPolicyPeer{{
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{servesLabel: app}},
		}}}},
	}}
}

// databaseURL is how the app's pods reach the installer's Postgres. The link is not encrypted:
// it stays inside the cluster, between pods the policy above names.
func databaseURL(app string, password []byte) []byte {
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(databaseUser, string(password)),
		Host: fmt.Sprintf("%s.%s.svc:%d", databaseName, namespaceOf(app), databasePort), Path: "/" + databaseUser,
		RawQuery: "sslmode=disable",
	}
	return []byte(u.String())
}

// extras are the Deployments beside "app" that exist in app's namespace, the replicas first.
func (c *Client) extras(ctx context.Context, app string) ([]*appsv1.Deployment, error) {
	var out []*appsv1.Deployment
	for _, name := range []string{replicaName, databaseName} {
		d, found, err := getOwned(ctx, c.cs.AppsV1().Deployments(namespaceOf(app)), "deployment", name)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, d)
		}
	}
	return out, nil
}

// replicaSecretsStep keeps the Secrets a replicated app starts from: the four keys and the
// first administrator's password, and the installer's database password and URL. Each value is
// created once and never replaced. An operator's database Secret is only checked.
type replicaSecretsStep struct{ a App }

func (s replicaSecretsStep) ID() string { return s.a.Name + ".replica-secrets" }
func (s replicaSecretsStep) InputHash() string {
	return hashOf("replica-secrets", s.a.databaseSecretName())
}

func (s replicaSecretsStep) secrets() writer[*corev1.Secret] {
	return s.a.Client.cs.CoreV1().Secrets(namespaceOf(s.a.Name))
}

func keyNames() []string {
	out := []string{bootstrapKey}
	for _, k := range replicaKeys {
		out = append(out, k.file)
	}
	return out
}

// check returns why the Secrets are not complete, "" when they are, registering every value
// for redaction.
func (s replicaSecretsStep) check(ctx context.Context) (string, error) {
	if err := s.a.replicaChange(ctx); err != nil {
		return "", err
	}
	keys, found, err := getOwned(ctx, s.secrets(), "secret", keysSecret)
	if err != nil {
		return "", err
	}
	if !found {
		return "secret " + keysSecret + " missing", nil
	}
	for _, n := range keyNames() {
		if len(keys.Data[n]) == 0 {
			return "secret " + keysSecret + " has no " + n, nil
		}
		s.a.Redact.Add(string(keys.Data[n]))
	}
	name := s.a.databaseSecretName()
	db, found, err := getOwned(ctx, s.secrets(), "secret", name)
	switch {
	case err != nil && !s.a.ownDatabase() && errors.Is(err, errNotOwned):
		return "", fmt.Errorf("database_secret %s is not labelled %s=%s: label the Secret you created to hand it to the installer", name, managed.Label, managed.Value)
	case err != nil:
		return "", err
	case !found:
		return "secret " + name + " missing", nil
	}
	raw := bytes.TrimSpace(db.Data[stack.DatabaseSecretKey])
	s.a.Redact.Add(string(raw))
	if s.a.ownDatabase() {
		s.a.Redact.Add(string(db.Data[passwordKey]))
		s.a.Redact.Add(string(db.Data[superuserKey]))
		if len(db.Data[passwordKey]) == 0 || len(db.Data[superuserKey]) == 0 || !bytes.Equal(raw, databaseURL(s.a.Name, db.Data[passwordKey])) {
			return "secret " + name + " is incomplete", nil
		}
		return "", nil
	}
	// The URL carries the password: an error names the Secret, never its contents.
	u, err := url.Parse(string(raw))
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" {
		return "", fmt.Errorf("database_secret %s: key %q must hold a postgres:// URL", name, stack.DatabaseSecretKey)
	}
	if pw, _ := u.User.Password(); pw != "" {
		s.a.Redact.Add(pw)
	}
	return "", nil
}

func (s replicaSecretsStep) Inspect(ctx context.Context) (bool, error) {
	why, err := s.check(ctx)
	return why == "", err
}

func (s replicaSecretsStep) Apply(ctx context.Context) error {
	if err := s.a.replicaChange(ctx); err != nil {
		return err
	}
	if err := s.a.ensureNamespace(ctx, false); err != nil {
		return err
	}
	ns := namespaceOf(s.a.Name)
	if err := s.fill(ctx, keysSecret, keyNames(), func(sec *corev1.Secret) error {
		if len(sec.Data[bootstrapKey]) == 0 {
			v, err := s.a.Client.generateCredentials(ctx, ns, s.a.ReleaseSet, 1)
			if err != nil {
				return err
			}
			sec.Data[bootstrapKey] = v[0]
		}
		if sec.Annotations[databaseAnnotation] == "" {
			sec.Annotations = map[string]string{databaseAnnotation: s.a.databaseRecord()}
		}
		if len(sec.Data[replicaKeys[0].file]) > 0 {
			return nil
		}
		keys, err := s.a.Client.generateKeys(ctx, s.a)
		if err != nil {
			return err
		}
		for i, k := range replicaKeys {
			sec.Data[k.file] = keys[i]
		}
		return nil
	}); err != nil {
		return err
	}
	if !s.a.ownDatabase() {
		// The namespace exists from here on, so the operator's Secret has somewhere to go.
		if why, err := s.check(ctx); err != nil || why == "" {
			return err
		}
		return fmt.Errorf("database_secret %[1]s does not exist in %[2]s. Create it with the Postgres URL under key %[3]q, label it, and run apply again:\n"+
			"  kubectl -n %[2]s create secret generic %[1]s --from-file=%[3]s=<a file holding the URL>\n"+
			"  kubectl -n %[2]s label secret %[1]s %[4]s=%[5]s",
			s.a.databaseSecretName(), ns, stack.DatabaseSecretKey, managed.Label, managed.Value)
	}
	return s.fill(ctx, databaseSecret, []string{passwordKey, superuserKey, stack.DatabaseSecretKey}, func(sec *corev1.Secret) error {
		for _, key := range []string{passwordKey, superuserKey} {
			if len(sec.Data[key]) > 0 {
				continue
			}
			v, err := s.a.Client.generateCredentials(ctx, ns, s.a.ReleaseSet, 1)
			if err != nil {
				return err
			}
			sec.Data[key] = v[0]
		}
		sec.Data[stack.DatabaseSecretKey] = databaseURL(s.a.Name, sec.Data[passwordKey])
		return nil
	})
}

// fill writes name once every one of keys has a value, leaving a complete Secret alone. The
// four keys are generated together, so a Secret holds all of them or none.
func (s replicaSecretsStep) fill(ctx context.Context, name string, keys []string, generate func(*corev1.Secret) error) error {
	sec, found, err := getOwned(ctx, s.secrets(), "secret", name)
	if err != nil {
		return err
	}
	if !found {
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespaceOf(s.a.Name)}, Type: corev1.SecretTypeOpaque}
	}
	if !slices.ContainsFunc(keys, func(k string) bool { return len(sec.Data[k]) == 0 }) {
		return nil
	}
	sec.Labels = workloadLabels(s.a.ReleaseSet)
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	if err := generate(sec); err != nil {
		return err
	}
	for _, k := range keys {
		s.a.Redact.Add(string(sec.Data[k]))
	}
	// Written against the resourceVersion just read, as secretsStep does.
	if found {
		_, err = s.secrets().Update(ctx, sec, metav1.UpdateOptions{})
	} else {
		_, err = s.secrets().Create(ctx, sec, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("secret %s: %w", name, engine.ErrTransient)
	case err != nil:
		return fmt.Errorf("secret %s: cluster refused credential publication", name)
	}
	return nil
}

func (s replicaSecretsStep) Verify(ctx context.Context) error {
	why, err := s.check(ctx)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}

// keyScratch is the generator's data directory, in memory.
const keyScratch = "/scratch/data"

// generateKeys has the product make its own four keys: a single instance of the app's image
// starts on a memory-backed scratch directory in the target cluster, generates them as any
// first start does, and is deleted once they are read. They are in the formats the release
// itself writes, and never on a node's disk or in a container log.
func (c *Client) generateKeys(ctx context.Context, a App) ([][]byte, error) {
	k := a.Catalog.Kubernetes
	single := a
	single.Catalog.Kubernetes = ptr.To(*k)
	single.Catalog.Kubernetes.ReplicaCount = 0
	app := render(single).Deployment.Spec.Template.Spec.Containers[0]
	app.Name = "generator"
	// The image's entrypoint is the server; the shell only makes its directory private first.
	app.Command = []string{"/bin/sh", "-ec", `umask 077; mkdir -p "$KYIDENTITY_DATA_DIR"; exec kyidentity "$@"`, "kyidentity"}
	for i := range app.Env {
		if app.Env[i].Name == "KYIDENTITY_DATA_DIR" {
			app.Env[i].Value = keyScratch
		}
	}
	app.VolumeMounts = []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "scratch", MountPath: "/scratch"}}
	app.StartupProbe, app.LivenessProbe = nil, nil // Ready, from the readiness path, is the signal
	memory := corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr.To(resource.MustParse("16Mi"))}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "kyq-keys-", Namespace: namespaceOf(a.Name), Labels: workloadLabels(a.ReleaseSet)},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         ptr.To(int64(180)),
			TerminationGracePeriodSeconds: ptr.To(int64(0)),
			AutomountServiceAccountToken:  ptr.To(false), EnableServiceLinks: ptr.To(false),
			SecurityContext: podSecurityContext(k),
			Containers:      []corev1.Container{app},
			Volumes: []corev1.Volume{
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &memory}},
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &memory}},
			},
		},
	}
	var keys [][]byte
	err := c.podSession(ctx, pod, "key generator", func(name string) error {
		for _, key := range replicaKeys {
			out, err := c.Exec(ctx, pod.Namespace, name, "generator", []string{"cat", keyScratch + "/" + key.file})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("cluster-local key generation failed")
			}
			if len(out) == 0 {
				return errors.New("key generator returned no " + key.file)
			}
			keys = append(keys, out)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// databaseAnnotation, on the keys Secret, records which database holds the install's accounts.
const databaseAnnotation = "kyquickstart/database"

// databaseRecord is the plan's database as databaseAnnotation writes it.
func (a App) databaseRecord() string {
	if a.ownDatabase() {
		return "installer"
	}
	return "secret/" + a.Catalog.Kubernetes.DatabaseSecret
}

func describeDatabase(record string) string {
	switch name, operators := strings.CutPrefix(record, "secret/"); {
	case operators:
		return "the Postgres that database_secret " + name + " names"
	case record == "installer":
		return "the installer's Postgres (Deployment " + databaseName + ")"
	}
	return "a database the installer has no record of"
}

// replicaChange refuses the moves the installer does not make: a single instance's data into
// Postgres, a replicated install back to one instance on an empty SQLite file, and a replicated
// install onto another database. Each would start KyIdentity with no accounts while the old
// ones sit unused.
func (a App) replicaChange(ctx context.Context) error {
	if k := a.Catalog.Kubernetes; k == nil || k.Replicas == nil {
		return nil
	}
	ns := namespaceOf(a.Name)
	sec, keys, err := getOwned(ctx, a.Client.cs.CoreV1().Secrets(ns), "secret", keysSecret)
	if err != nil {
		return err
	}
	switch {
	case keys && !a.replicated():
		return fmt.Errorf("%s is installed as replicas on Postgres and the plan no longer has replicas: restore its replicas, or move it back by hand with a capsule (KyIdentity's docs/RUNNING.md)", a.Name)
	case keys && sec.Annotations[databaseAnnotation] != a.databaseRecord():
		return fmt.Errorf("%[1]s keeps its accounts in %[2]s and the plan names %[3]s. The installer does not move a database: %[1]s would start with no accounts and the old ones would sit unused. Put database_secret back as it was. If you moved the data yourself, or only renamed the Secret, record that and run apply again (the installer deletes no database; an unused one is yours to remove):\n"+
			"  kubectl -n %[4]s annotate secret %[5]s %[6]s=%[7]s --overwrite",
			a.Name, describeDatabase(sec.Annotations[databaseAnnotation]), describeDatabase(a.databaseRecord()), ns, keysSecret, databaseAnnotation, a.databaseRecord())
	case keys || !a.replicated():
		return nil
	}
	// No keys yet, so nothing of the app may exist: new keys beside old data would orphan it.
	claims := []string{"vol-" + databaseName}
	for _, v := range a.Catalog.Kubernetes.Volumes {
		claims = append(claims, "vol-"+v.Name)
	}
	for _, claim := range claims {
		if _, found, err := getOwned(ctx, a.Client.cs.CoreV1().PersistentVolumeClaims(ns), "claim", claim); err != nil {
			return err
		} else if found {
			return fmt.Errorf("%[1]s already has data (claim %[2]s) and no Secret %[3]s. If it was a single instance, the installer does not yet move it into Postgres: remove replicas from the plan, or move it by hand with migrate-to-postgres (KyIdentity's docs/RUNNING.md, \"Moving an install from SQLite to Postgres\"). If it ran as replicas, restore Secret %[3]s: new keys would end every session and orphan every sealed secret", a.Name, claim, keysSecret)
		}
	}
	return nil
}
