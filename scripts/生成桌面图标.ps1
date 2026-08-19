$ErrorActionPreference = 'Stop'

Add-Type -AssemblyName System.Drawing

$output = Join-Path $PSScriptRoot '..\desktop\lan-drop.ico'
$output = [IO.Path]::GetFullPath($output)
$sizes = @(16, 24, 32, 48, 64, 128, 256)
$frames = @()

function New-RoundedPath([float]$x, [float]$y, [float]$width, [float]$height, [float]$radius) {
    $path = New-Object System.Drawing.Drawing2D.GraphicsPath
    $diameter = $radius * 2
    $path.AddArc($x, $y, $diameter, $diameter, 180, 90)
    $path.AddArc($x + $width - $diameter, $y, $diameter, $diameter, 270, 90)
    $path.AddArc($x + $width - $diameter, $y + $height - $diameter, $diameter, $diameter, 0, 90)
    $path.AddArc($x, $y + $height - $diameter, $diameter, $diameter, 90, 90)
    $path.CloseFigure()
    return $path
}

foreach ($size in $sizes) {
    $bitmap = New-Object System.Drawing.Bitmap $size, $size, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $graphics.Clear([System.Drawing.Color]::Transparent)

    $scale = $size / 256.0
    $backgroundPath = New-RoundedPath (18 * $scale) (18 * $scale) (220 * $scale) (220 * $scale) (48 * $scale)
    $backgroundBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 18, 35, 58))
    $graphics.FillPath($backgroundBrush, $backgroundPath)
    $borderPen = New-Object System.Drawing.Pen ([System.Drawing.Color]::FromArgb(255, 50, 119, 143)), (6 * $scale)
    $graphics.DrawPath($borderPen, $backgroundPath)

    $nodePen = New-Object System.Drawing.Pen ([System.Drawing.Color]::FromArgb(210, 78, 210, 196)), (8 * $scale)
    $nodeBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 98, 225, 202))
    $center = New-Object System.Drawing.PointF (128 * $scale), (101 * $scale)
    foreach ($point in @(
        (New-Object System.Drawing.PointF (82 * $scale), (72 * $scale)),
        (New-Object System.Drawing.PointF (128 * $scale), (51 * $scale)),
        (New-Object System.Drawing.PointF (174 * $scale), (72 * $scale))
    )) {
        $graphics.DrawLine($nodePen, $point, $center)
    }
    foreach ($point in @(
        (New-Object System.Drawing.PointF (82 * $scale), (72 * $scale)),
        (New-Object System.Drawing.PointF (128 * $scale), (51 * $scale)),
        (New-Object System.Drawing.PointF (174 * $scale), (72 * $scale)),
        $center
    )) {
        $radius = if ($point -eq $center) { 12 * $scale } else { 10 * $scale }
        $graphics.FillEllipse($nodeBrush, $point.X - $radius, $point.Y - $radius, $radius * 2, $radius * 2)
    }

    $arrowBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 80, 224, 192))
    $arrow = New-Object System.Drawing.Drawing2D.GraphicsPath
    $arrowPoints = [System.Drawing.PointF[]] @(
        (New-Object System.Drawing.PointF (118 * $scale), (91 * $scale)),
        (New-Object System.Drawing.PointF (138 * $scale), (91 * $scale)),
        (New-Object System.Drawing.PointF (138 * $scale), (139 * $scale)),
        (New-Object System.Drawing.PointF (161 * $scale), (139 * $scale)),
        (New-Object System.Drawing.PointF (128 * $scale), (174 * $scale)),
        (New-Object System.Drawing.PointF (95 * $scale), (139 * $scale)),
        (New-Object System.Drawing.PointF (118 * $scale), (139 * $scale))
    )
    $arrow.AddPolygon($arrowPoints)
    $graphics.FillPath($arrowBrush, $arrow)

    $trayPath = New-RoundedPath (69 * $scale) (177 * $scale) (118 * $scale) (28 * $scale) (10 * $scale)
    $trayBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255, 237, 252, 247))
    $graphics.FillPath($trayBrush, $trayPath)
    $graphics.FillRectangle($trayBrush, 61 * $scale, 177 * $scale, 16 * $scale, 8 * $scale)
    $graphics.FillRectangle($trayBrush, 179 * $scale, 177 * $scale, 16 * $scale, 8 * $scale)

    $stream = New-Object IO.MemoryStream
    $bitmap.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
    $frames += ,$stream.ToArray()
    $stream.Dispose()
    $arrow.Dispose(); $trayPath.Dispose(); $backgroundPath.Dispose(); $backgroundBrush.Dispose(); $borderPen.Dispose(); $nodePen.Dispose(); $nodeBrush.Dispose(); $arrowBrush.Dispose(); $trayBrush.Dispose(); $graphics.Dispose(); $bitmap.Dispose()
}

$directory = [IO.Directory]::GetParent($output)
$directory.Create()
$ico = New-Object IO.MemoryStream
$writer = New-Object IO.BinaryWriter $ico
$writer.Write([UInt16]0)
$writer.Write([UInt16]1)
$writer.Write([UInt16]$sizes.Count)
$offset = 6 + (16 * $sizes.Count)
for ($index = 0; $index -lt $sizes.Count; $index++) {
    $size = $sizes[$index]
    $dimension = if ($size -eq 256) { 0 } else { $size }
    $writer.Write([Byte]$dimension)
    $writer.Write([Byte]$dimension)
    $writer.Write([Byte]0)
    $writer.Write([Byte]0)
    $writer.Write([UInt16]1)
    $writer.Write([UInt16]32)
    $writer.Write([UInt32]$frames[$index].Length)
    $writer.Write([UInt32]$offset)
    $offset += $frames[$index].Length
}
foreach ($frame in $frames) { $writer.Write($frame) }
[IO.File]::WriteAllBytes($output, $ico.ToArray())
$writer.Dispose(); $ico.Dispose()
Write-Output "Generated $output"
