package io.caliper.apm.gradle

import com.android.build.api.instrumentation.AsmClassVisitorFactory
import com.android.build.api.instrumentation.ClassContext
import com.android.build.api.instrumentation.ClassData
import com.android.build.api.instrumentation.FramesComputationMode
import com.android.build.api.instrumentation.InstrumentationParameters
import com.android.build.api.instrumentation.InstrumentationScope
import com.android.build.api.variant.AndroidComponentsExtension
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.provider.ListProperty
import org.gradle.api.provider.Property
import org.gradle.api.tasks.Input
import org.objectweb.asm.AnnotationVisitor
import org.objectweb.asm.ClassReader
import org.objectweb.asm.ClassVisitor
import org.objectweb.asm.ClassWriter
import org.objectweb.asm.MethodVisitor
import org.objectweb.asm.Opcodes
import org.objectweb.asm.Type
import org.objectweb.asm.commons.AdviceAdapter

/**
 * Gradle DSL for `io.caliper.apm.autoinstrument`:
 * ```kotlin
 * caliperAutoInstrument {
 *     enabled.set(true)
 *     instrumentOkHttp.set(true)
 *     instrumentCaliperTrace.set(true)
 *     includePackages.set(listOf("com.example.app"))
 * }
 * ```
 */
abstract class CaliperAutoInstrumentExtension {
    abstract val enabled: Property<Boolean>
    abstract val instrumentOkHttp: Property<Boolean>
    abstract val instrumentCaliperTrace: Property<Boolean>
    abstract val includePackages: ListProperty<String>
    abstract val excludePackages: ListProperty<String>

    init {
        enabled.convention(true)
        instrumentOkHttp.convention(true)
        instrumentCaliperTrace.convention(true)
        includePackages.convention(emptyList())
        excludePackages.convention(emptyList())
    }
}

interface CaliperAsmParams : InstrumentationParameters {
    @get:Input val instrumentOkHttp: Property<Boolean>
    @get:Input val instrumentCaliperTrace: Property<Boolean>
    @get:Input val includePackages: ListProperty<String>
    @get:Input val excludePackages: ListProperty<String>
}

data class CaliperAsmConfig(
    val instrumentOkHttp: Boolean = true,
    val instrumentCaliperTrace: Boolean = true,
    val includePackages: List<String> = emptyList(),
    val excludePackages: List<String> = emptyList(),
    val okHttpBuilderOwner: String = "okhttp3/OkHttpClient\$Builder",
    val okHttpBuildDesc: String = "()Lokhttp3/OkHttpClient;",
    val hooksOwner: String = "io/caliper/apm/CaliperAutoInstrumentHooks",
    val traceAnnotationDesc: String = "Lio/caliper/apm/CaliperTrace;",
)

/**
 * Gradle plugin `io.caliper.apm.autoinstrument` registering an AGP 8.x `AsmClassVisitorFactory`
 * for safe allowlisted OkHttp + `@CaliperTrace` bytecode weaving.
 */
class CaliperAutoInstrumentPlugin : Plugin<Project> {
    override fun apply(project: Project) {
        val ext = project.extensions.create("caliperAutoInstrument", CaliperAutoInstrumentExtension::class.java)

        project.plugins.withId("com.android.application") {
            val androidComponents = project.extensions.findByType(AndroidComponentsExtension::class.java)
            androidComponents?.onVariants { variant ->
                if (!ext.enabled.get()) return@onVariants
                variant.instrumentation.transformClassesWith(
                    CaliperAsmClassVisitorFactory::class.java,
                    InstrumentationScope.ALL,
                ) { params ->
                    params.instrumentOkHttp.set(ext.instrumentOkHttp)
                    params.instrumentCaliperTrace.set(ext.instrumentCaliperTrace)
                    params.includePackages.set(ext.includePackages)
                    params.excludePackages.set(ext.excludePackages)
                }
                variant.instrumentation.setAsmFramesComputationMode(
                    FramesComputationMode.COMPUTE_FRAMES_FOR_INSTRUMENTED_METHODS
                )
            }
        }
    }
}

abstract class CaliperAsmClassVisitorFactory : AsmClassVisitorFactory<CaliperAsmParams> {
    override fun createClassVisitor(classContext: ClassContext, nextClassVisitor: ClassVisitor): ClassVisitor {
        val cfg = CaliperAsmConfig(
            instrumentOkHttp = parameters.get().instrumentOkHttp.get(),
            instrumentCaliperTrace = parameters.get().instrumentCaliperTrace.get(),
            includePackages = parameters.get().includePackages.get(),
            excludePackages = parameters.get().excludePackages.get(),
        )
        return CaliperAsmClassVisitor(Opcodes.ASM9, nextClassVisitor, cfg)
    }

    override fun isInstrumentable(classData: ClassData): Boolean {
        val p = parameters.get()
        return CaliperAsmFilter.shouldInstrument(
            className = classData.className,
            includePackages = p.includePackages.get(),
            excludePackages = p.excludePackages.get(),
        )
    }
}

object CaliperAsmFilter {
    private val hardExcludedPrefixes = listOf(
        "io.caliper.apm.",
        "android.",
        "androidx.",
        "kotlin.",
        "kotlinx.",
        "java.",
        "javax.",
        "sun.",
        "okhttp3.",
        "okio.",
    )

    fun shouldInstrument(
        className: String,
        includePackages: List<String> = emptyList(),
        excludePackages: List<String> = emptyList(),
    ): Boolean {
        val dotted = className.replace('/', '.')
        if (hardExcludedPrefixes.any { dotted.startsWith(it) }) return false
        if (excludePackages.any { it.isNotBlank() && dotted.startsWith(it) }) return false
        if (includePackages.isNotEmpty()) {
            return includePackages.any { it.isNotBlank() && dotted.startsWith(it) }
        }
        return true
    }
}

/**
 * Pure ASM9 bytecode visitor implementing:
 * 1. OkHttp auto-hook: injects `CaliperAutoInstrumentHooks.onOkHttpBuilder(builder)` right before
 *    `OkHttpClient$Builder.build()`.
 * 2. `@CaliperTrace` method wrapper: injects `beginTrace()` on method enter and `endTrace(..., failed)`
 *    on every return/throw exit path via `AdviceAdapter`.
 */
class CaliperAsmClassVisitor(
    api: Int,
    next: ClassVisitor,
    private val cfg: CaliperAsmConfig = CaliperAsmConfig(),
) : ClassVisitor(api, next) {

    private var internalClassName: String = ""
    private var skipClass: Boolean = false

    override fun visit(
        version: Int,
        access: Int,
        name: String,
        signature: String?,
        superName: String?,
        interfaces: Array<out String>?,
    ) {
        internalClassName = name
        skipClass = !CaliperAsmFilter.shouldInstrument(name, cfg.includePackages, cfg.excludePackages)
        super.visit(version, access, name, signature, superName, interfaces)
    }

    override fun visitMethod(
        access: Int,
        name: String,
        descriptor: String,
        signature: String?,
        exceptions: Array<out String>?,
    ): MethodVisitor {
        val mv = super.visitMethod(access, name, descriptor, signature, exceptions)
        if (skipClass || (access and Opcodes.ACC_ABSTRACT) != 0 || (access and Opcodes.ACC_NATIVE) != 0) {
            return mv
        }
        return CaliperAdviceAdapter(api, mv, access, name, descriptor, internalClassName, cfg)
    }

    companion object {
        /** Convenience entry point for standalone bytecode transformation & JVM verification tests. */
        fun transformBytes(classBytes: ByteArray, cfg: CaliperAsmConfig = CaliperAsmConfig()): ByteArray {
            val cr = ClassReader(classBytes)
            val cw = ClassWriter(cr, ClassWriter.COMPUTE_FRAMES)
            cr.accept(CaliperAsmClassVisitor(Opcodes.ASM9, cw, cfg), ClassReader.EXPAND_FRAMES)
            return cw.toByteArray()
        }
    }
}

private class CaliperAdviceAdapter(
    api: Int,
    mv: MethodVisitor,
    access: Int,
    private val methodName: String,
    descriptor: String,
    private val ownerInternal: String,
    private val cfg: CaliperAsmConfig,
) : AdviceAdapter(api, mv, access, methodName, descriptor) {

    private var hasTraceAnnotation = false
    private var traceName = ""
    private var traceScene = ""
    private var startTimeLocal = -1

    override fun visitAnnotation(descriptor: String, visible: Boolean): AnnotationVisitor? {
        val av = super.visitAnnotation(descriptor, visible)
        if (cfg.instrumentCaliperTrace && descriptor == cfg.traceAnnotationDesc) {
            hasTraceAnnotation = true
            return object : AnnotationVisitor(api, av) {
                override fun visit(name: String?, value: Any?) {
                    when (name) {
                        "name" -> traceName = value?.toString() ?: ""
                        "scene" -> traceScene = value?.toString() ?: ""
                    }
                    super.visit(name, value)
                }
            }
        }
        return av
    }

    override fun onMethodEnter() {
        if (!hasTraceAnnotation) return
        val resolvedName = traceName.ifEmpty { "${ownerInternal.substringAfterLast('/')}.$methodName" }
        startTimeLocal = newLocal(Type.LONG_TYPE)
        mv.visitLdcInsn(resolvedName)
        mv.visitMethodInsn(
            Opcodes.INVOKESTATIC,
            cfg.hooksOwner,
            "beginTrace",
            "(Ljava/lang/String;)J",
            false,
        )
        mv.visitVarInsn(Opcodes.LSTORE, startTimeLocal)
    }

    override fun visitMethodInsn(
        opcodeAndSource: Int,
        owner: String,
        name: String,
        descriptor: String,
        isInterface: Boolean,
    ) {
        if (cfg.instrumentOkHttp &&
            opcodeAndSource == Opcodes.INVOKEVIRTUAL &&
            owner == cfg.okHttpBuilderOwner &&
            name == "build" &&
            descriptor == cfg.okHttpBuildDesc
        ) {
            // Stack top is `OkHttpClient$Builder`; pass through static hook and cast back before build().
            mv.visitMethodInsn(
                Opcodes.INVOKESTATIC,
                cfg.hooksOwner,
                "onOkHttpBuilder",
                "(Ljava/lang/Object;)Ljava/lang/Object;",
                false,
            )
            mv.visitTypeInsn(Opcodes.CHECKCAST, cfg.okHttpBuilderOwner)
        }
        super.visitMethodInsn(opcodeAndSource, owner, name, descriptor, isInterface)
    }

    override fun onMethodExit(opcode: Int) {
        if (!hasTraceAnnotation || startTimeLocal < 0) return
        val resolvedName = traceName.ifEmpty { "${ownerInternal.substringAfterLast('/')}.$methodName" }
        mv.visitLdcInsn(resolvedName)
        mv.visitLdcInsn(traceScene)
        mv.visitVarInsn(Opcodes.LLOAD, startTimeLocal)
        mv.visitInsn(if (opcode == Opcodes.ATHROW) Opcodes.ICONST_1 else Opcodes.ICONST_0)
        mv.visitMethodInsn(
            Opcodes.INVOKESTATIC,
            cfg.hooksOwner,
            "endTrace",
            "(Ljava/lang/String;Ljava/lang/String;JZ)V",
            false,
        )
    }
}
