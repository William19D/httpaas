# Mockup del Dashboard

`dashboard-mockup.html` es un mockup HTML estático y autocontenido (sin servidor, sin dependencias externas más allá de Google Fonts) que muestra la interfaz aprobada antes del desarrollo.

Abrir directamente en el navegador:

```
firefox  mockup/dashboard-mockup.html
chromium mockup/dashboard-mockup.html
```

## Paleta y tipografía

- Estética **brutalist-terminal**: bordes sólidos de 1.5–2 px, sin sombras, sin esquinas redondeadas (excepto el detalle del botón principal).
- Paleta beige cálida + tinta negra + acento lima eléctrico (`#c6ff3a`).
- Tipografías: **Space Grotesk** (displays/títulos) y **JetBrains Mono** (cuerpo y datos).
- Cuadrícula sutil de fondo (24 px) que evoca la línea de comandos.

## Decisiones de diseño justificadas

| Decisión | Razón |
|---|---|
| Color verde-lima para indicar acciones primarias y métricas destacadas | Alto contraste sobre el fondo cálido + distintivo (no es ni azul ni gris corporativo) |
| Estado de instancia como **chip con dot** que cambia de color | Patrón estándar en consolas cloud (AWS, GCP, Vercel) |
| Tabla con bordes finos y filas con hover | Densidad de información alta sin ser opresiva |
| Métricas en cuatro tarjetas alineadas | Resumen instantáneo del sistema antes de leer la tabla |
| Banner negro arriba en el mockup | Avisa visualmente que es un mockup, no producto final |

## Comparación con la app final

La estructura es idéntica. El mockup omite:

- El polling JS que actualiza la tabla cada 3 s.
- La paginación (irrelevante para 3-5 instancias).
- Vistas auxiliares: `/instances/{id}` con el log detallado.

Estas tres se ven en la webapp real una vez instalada.
