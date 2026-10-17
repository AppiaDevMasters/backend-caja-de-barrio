-- Datos del demo (contrato, sección 4): cinco tiendas en Aranjuez, Medellín, y las dos
-- clientas. Lo carga deploy/instalar.sh después de que el backend corre sus migraciones.
-- Es idempotente: se puede correr varias veces. :'base' es la base pública de las wallet
-- addresses, por ejemplo https://op.cajadebarrio.lat
--
-- Los WhatsApp son los ficticios de las pruebas. Para la demo por WhatsApp hay que
-- cambiarlos por los celulares reales (ver contexto-caja-de-barrio/guia-whatsapp-meta.md).

insert into tiendas (id, nombre, direccion, ubicacion, nivel, estado, wallet_address,
                     whatsapp_tendero, nombre_tendero, reputacion)
values
	('tie_jaime', 'Tienda Don Jaime', 'Cra 50 #92-15', ST_MakePoint(-75.55800, 6.27610)::geography,
	 'comercio', 'activa', :'base' || '/tienda-cajadebarrio', '573000000010', 'Don Jaime', 4.90),
	('tie_paisa', 'Minimercado El Paisa', 'Cra 51 #90-22', ST_MakePoint(-75.55790, 6.27560)::geography,
	 'comercio', 'activa', :'base' || '/tienda-cajadebarrio', '573000000011', 'Don Ramiro', 4.70),
	('tie_gloria', 'Tienda Doña Gloria', 'Cl 93 #49-40', ST_MakePoint(-75.55710, 6.28010)::geography,
	 'basico', 'activa', :'base' || '/tienda-cajadebarrio', '573000000012', 'Doña Gloria', 4.50),
	('tie_esperanza', 'Granero La Esperanza', 'Cl 95 #50-10', ST_MakePoint(-75.55640, 6.28220)::geography,
	 'aliado', 'activa', :'base' || '/tienda-cajadebarrio', '573000000013', 'Doña Marta', 4.80),
	('tie_aranjuez', 'Autoservicio Aranjuez', 'Cra 49 #88-05', ST_MakePoint(-75.55520, 6.27310)::geography,
	 'basico', 'activa', :'base' || '/tienda-cajadebarrio', '573000000014', 'Don Luis', 4.20)
on conflict (id) do update set wallet_address = excluded.wallet_address;

insert into personas (id, whatsapp, cedula, nombre, wallet_address, nivel, estado_registro)
values
	('per_laura', '573000000001', '1036123456', 'Laura Gómez', :'base' || '/laura-cajadebarrio', 1, 'aprobada'),
	('per_rosa', '573000000002', '43123456', 'Rosa Restrepo', :'base' || '/andres-cajadebarrio', 1, 'aprobada')
on conflict (id) do update set wallet_address = excluded.wallet_address;

-- Efectivo publicado para hoy y mañana en la hora de Colombia (el día que usa el backend).
-- No pisa lo que ya esté reservado.
insert into efectivo_disponible (tienda_id, fecha, publicado_cop, reservado_cop)
select t.id, d.fecha, t.publicado, 0
from (values ('tie_jaime', 800000), ('tie_paisa', 600000), ('tie_gloria', 250000),
             ('tie_esperanza', 1500000), ('tie_aranjuez', 150000)) as t (id, publicado)
cross join (select (now() at time zone 'America/Bogota')::date + n as fecha
            from generate_series(0, 1) as n) as d
on conflict (tienda_id, fecha) do nothing;
